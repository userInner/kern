// Package plugin defines the versioned Kern plugin contract.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion    = "1"
	ManifestFile     = "kern.plugin.json"
	CoreVersion      = "0.1.0"
	HostIDPrefix     = "host."
	maxManifestBytes = 256 << 10
	maxPackageFiles  = 2_000
	maxPackageBytes  = 128 << 20
)

var (
	ErrInvalidManifest = errors.New("plugin: invalid manifest")
	ErrIncompatible    = errors.New("plugin: incompatible core version")
	ErrIntegrity       = errors.New("plugin: package integrity mismatch")
	ErrNotFound        = errors.New("plugin: not found")
	ErrConflict        = errors.New("plugin: conflicting version already installed")
	pluginIDPattern    = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+$`)
	semverPattern      = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)
	intentPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+$`)
)

// Manifest is the immutable package declaration installed by Kern.
type Manifest struct {
	SchemaVersion string      `json:"schema_version"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Description   string      `json:"description,omitempty"`
	Version       string      `json:"version"`
	Core          string      `json:"core"`
	Entrypoints   Entrypoints `json:"entrypoints"`
	Activation    Activation  `json:"activation"`
	Permissions   Permissions `json:"permissions"`
	Integrity     Integrity   `json:"integrity"`
}

type Entrypoints struct {
	Knowledge []string `json:"knowledge,omitempty"`
	Workflows []string `json:"workflows,omitempty"`
	Rules     []string `json:"rules,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	Verifiers []string `json:"verifiers,omitempty"`
	Evals     []string `json:"evals,omitempty"`
}

type Activation struct {
	Signals []string `json:"signals,omitempty"`
	Intents []string `json:"intents,omitempty"`
}

type Permissions struct {
	Filesystem []string `json:"filesystem,omitempty"`
	Process    []string `json:"process,omitempty"`
}

type Integrity struct {
	Files string `json:"files"`
}

// Installed is the public-safe durable plugin snapshot.
type Installed struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Version       string    `json:"version"`
	Source        string    `json:"source"`
	Digest        string    `json:"digest"`
	Enabled       bool      `json:"enabled"`
	TrustStatus   string    `json:"trust_status"`
	Manifest      Manifest  `json:"manifest"`
	InstalledAt   time.Time `json:"installed_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	InstallPath   string    `json:"-"`
}

// LoadManifest strictly decodes and validates a plugin manifest.
func LoadManifest(directory string) (Manifest, []byte, error) {
	file, err := os.Open(filepath.Join(directory, ManifestFile))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("plugin: opening manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("plugin: reading manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return Manifest{}, nil, fmt.Errorf("%w: manifest exceeds 256 KiB", ErrInvalidManifest)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, nil, fmt.Errorf("%w: manifest must contain one JSON object", ErrInvalidManifest)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, nil, err
	}
	return manifest, data, nil
}

// ValidateManifest validates identifiers, compatibility, permissions, and paths.
func ValidateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %q", ErrInvalidManifest, manifest.SchemaVersion)
	}
	if !pluginIDPattern.MatchString(manifest.ID) || len(manifest.ID) > 200 {
		return fmt.Errorf("%w: id must be a reverse-domain identifier", ErrInvalidManifest)
	}
	if strings.HasPrefix(manifest.ID, HostIDPrefix) {
		return fmt.Errorf("%w: id prefix %q is reserved for host capabilities", ErrInvalidManifest, HostIDPrefix)
	}
	if strings.TrimSpace(manifest.Name) == "" || len([]rune(manifest.Name)) > 100 ||
		len([]rune(manifest.Description)) > 1_000 {
		return fmt.Errorf("%w: invalid name or description", ErrInvalidManifest)
	}
	if !semverPattern.MatchString(manifest.Version) {
		return fmt.Errorf("%w: version must be semantic versioning", ErrInvalidManifest)
	}
	compatible, err := Compatible(manifest.Core, CoreVersion)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if !compatible {
		return fmt.Errorf("%w: plugin requires %s, core is %s", ErrIncompatible, manifest.Core, CoreVersion)
	}
	paths := manifest.Entrypoints.all()
	if len(paths) == 0 || len(paths) > 500 {
		return fmt.Errorf("%w: entrypoints must contain 1 to 500 files", ErrInvalidManifest)
	}
	seen := make(map[string]bool, len(paths))
	for _, item := range paths {
		if err := validateRelativePath(item); err != nil {
			return err
		}
		if seen[item] {
			return fmt.Errorf("%w: duplicate entrypoint %q", ErrInvalidManifest, item)
		}
		seen[item] = true
	}
	for _, permission := range manifest.Permissions.Filesystem {
		if permission != "workspace:read" && permission != "workspace:write" {
			return fmt.Errorf("%w: unsupported filesystem permission %q", ErrInvalidManifest, permission)
		}
	}
	for _, executable := range manifest.Permissions.Process {
		if strings.TrimSpace(executable) == "" || strings.ContainsAny(executable, `/\\\x00`) {
			return fmt.Errorf("%w: invalid process permission %q", ErrInvalidManifest, executable)
		}
	}
	for _, signal := range manifest.Activation.Signals {
		if strings.TrimSpace(signal) != signal || signal == "" || len(signal) > 256 ||
			strings.ContainsAny(signal, `\`+"\x00") || path.IsAbs(signal) ||
			strings.HasPrefix(path.Clean(signal), "../") {
			return fmt.Errorf("%w: invalid activation signal %q", ErrInvalidManifest, signal)
		}
		if _, err := path.Match(strings.ReplaceAll(signal, "**", "*"), "probe"); err != nil {
			return fmt.Errorf("%w: invalid activation signal %q", ErrInvalidManifest, signal)
		}
	}
	for _, intent := range manifest.Activation.Intents {
		if !intentPattern.MatchString(intent) || len(intent) > 100 {
			return fmt.Errorf("%w: invalid activation intent %q", ErrInvalidManifest, intent)
		}
	}
	if !strings.HasPrefix(manifest.Integrity.Files, "sha256:") ||
		len(strings.TrimPrefix(manifest.Integrity.Files, "sha256:")) != sha256.Size*2 {
		return fmt.Errorf("%w: integrity.files must be sha256:<hex>", ErrInvalidManifest)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(manifest.Integrity.Files, "sha256:")); err != nil {
		return fmt.Errorf("%w: integrity.files is not hexadecimal", ErrInvalidManifest)
	}
	return nil
}

func (e Entrypoints) all() []string {
	items := make([]string, 0, len(e.Knowledge)+len(e.Workflows)+len(e.Rules)+len(e.Tools)+len(e.Verifiers)+len(e.Evals))
	items = append(items, e.Knowledge...)
	items = append(items, e.Workflows...)
	items = append(items, e.Rules...)
	items = append(items, e.Tools...)
	items = append(items, e.Verifiers...)
	items = append(items, e.Evals...)
	return items
}

func validateRelativePath(value string) error {
	if value == "" || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value ||
		value == "." || strings.HasPrefix(value, "../") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%w: unsafe entrypoint path %q", ErrInvalidManifest, value)
	}
	return nil
}

// PackageDigest hashes every regular package file except the self-referential manifest.
func PackageDigest(directory string) (string, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("plugin: resolving package directory: %w", err)
	}
	type entry struct {
		path   string
		digest [sha256.Size]byte
		size   int64
	}
	entries := make([]entry, 0)
	var total int64
	err = filepath.WalkDir(directory, func(filePath string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(directory, filePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin: symbolic links are not allowed: %s", relative)
		}
		if item.IsDir() {
			return nil
		}
		if !item.Type().IsRegular() {
			return fmt.Errorf("plugin: non-regular file is not allowed: %s", relative)
		}
		relative = filepath.ToSlash(relative)
		if relative == ManifestFile {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if len(entries) >= maxPackageFiles || total > maxPackageBytes {
			return errors.New("plugin: package exceeds file or byte limit")
		}
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		var digest [sha256.Size]byte
		copy(digest[:], hash.Sum(nil))
		entries = append(entries, entry{path: relative, digest: digest, size: info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", errors.New("plugin: package contains no payload files")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	for _, item := range entries {
		fmt.Fprintf(hash, "%s\x00%x\n", item.path, item.digest)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyPackage verifies integrity and every declared entrypoint.
func VerifyPackage(directory string, manifest Manifest) (string, error) {
	for _, relative := range manifest.Entrypoints.all() {
		info, err := os.Lstat(filepath.Join(directory, filepath.FromSlash(relative)))
		if err != nil {
			return "", fmt.Errorf("%w: entrypoint %q: %v", ErrInvalidManifest, relative, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: entrypoint %q is not a regular file", ErrInvalidManifest, relative)
		}
	}
	digest, err := PackageDigest(directory)
	if err != nil {
		return "", err
	}
	if digest != manifest.Integrity.Files {
		return "", fmt.Errorf("%w: got %s, want %s", ErrIntegrity, digest, manifest.Integrity.Files)
	}
	return digest, nil
}

// Compatible evaluates the supported v1 range form: >=x.y.z <x.y.z.
func Compatible(requirement, current string) (bool, error) {
	parts := strings.Fields(requirement)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], ">=") || !strings.HasPrefix(parts[1], "<") {
		return false, errors.New("core constraint must use >=x.y.z <x.y.z")
	}
	minimum, err := parseVersion(strings.TrimPrefix(parts[0], ">="))
	if err != nil {
		return false, err
	}
	maximum, err := parseVersion(strings.TrimPrefix(parts[1], "<"))
	if err != nil {
		return false, err
	}
	version, err := parseVersion(current)
	if err != nil {
		return false, err
	}
	return compareVersion(version, minimum) >= 0 && compareVersion(version, maximum) < 0, nil
}

type version [3]int

func parseVersion(value string) (version, error) {
	match := semverPattern.FindStringSubmatch(value)
	if match == nil {
		return version{}, fmt.Errorf("invalid semantic version %q", value)
	}
	var parsed version
	for index := range parsed {
		if _, err := fmt.Sscan(match[index+1], &parsed[index]); err != nil {
			return version{}, err
		}
	}
	return parsed, nil
}

func compareVersion(left, right version) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}
