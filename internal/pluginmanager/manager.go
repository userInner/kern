// Package pluginmanager owns safe local plugin installation and lifecycle.
package pluginmanager

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

var (
	// ErrInvalidSource identifies a plugin source rejected at the import boundary.
	ErrInvalidSource = errors.New("pluginmanager: invalid source")
	// ErrClosed identifies an operation attempted after Manager.Close.
	ErrClosed = errors.New("pluginmanager: closed")
)

type repository interface {
	InstallPlugin(ctx context.Context, item plugin.Installed) (plugin.Installed, bool, error)
	GetPlugin(ctx context.Context, pluginID string) (plugin.Installed, error)
	ListPlugins(ctx context.Context) ([]plugin.Installed, error)
	SetPluginEnabled(ctx context.Context, pluginID string, enabled bool) (plugin.Installed, error)
	DeletePlugin(ctx context.Context, pluginID string) error
}

// Manager owns plugin files beneath one private root.
type Manager struct {
	root      string
	rootInfo  fs.FileInfo
	storage   *os.Root
	repo      repository
	lifecycle sync.RWMutex
	closed    bool
	closeErr  error
}

// New constructs a plugin manager and restricts its storage directory.
func New(root string, repo repository) (*Manager, error) {
	if strings.TrimSpace(root) == "" || repo == nil {
		return nil, errors.New("pluginmanager: root and repository are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("pluginmanager: resolving root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("pluginmanager: creating root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("pluginmanager: resolving root links: %w", err)
	}
	if err := os.Chmod(canonical, 0o700); err != nil {
		return nil, fmt.Errorf("pluginmanager: restricting root: %w", err)
	}
	storage, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("pluginmanager: opening root: %w", err)
	}
	rootInfo, err := storage.Stat(".")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("pluginmanager: inspecting root: %w", err), storage.Close())
	}
	return &Manager{root: canonical, rootInfo: rootInfo, storage: storage, repo: repo}, nil
}

// Close waits for active plugin operations and releases the stable storage-root
// capability. It is safe to call concurrently and repeatedly.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	m.closeErr = m.storage.Close()
	return m.closeErr
}

func (m *Manager) beginOperation() error {
	m.lifecycle.RLock()
	if m.closed {
		m.lifecycle.RUnlock()
		return ErrClosed
	}
	return nil
}

func (m *Manager) endOperation() {
	m.lifecycle.RUnlock()
}

// Install validates, verifies, copies, and durably records a local package
// selected by a trusted local operator. Network handlers must use
// InstallFromRoot so request data cannot select an arbitrary host path.
func (m *Manager) Install(ctx context.Context, source string) (plugin.Installed, bool, error) {
	if err := m.beginOperation(); err != nil {
		return plugin.Installed{}, false, err
	}
	defer m.endOperation()
	if err := ctx.Err(); err != nil {
		return plugin.Installed{}, false, err
	}
	source = strings.TrimSpace(source)
	if source == "" || strings.ContainsRune(source, 0) {
		return plugin.Installed{}, false, errors.New("pluginmanager: source is required")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: resolving source: %w", err)
	}
	source, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: resolving source links: %w", err)
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: opening source: %w", err)
	}
	installed, created, installErr := m.install(ctx, sourceRoot, source, false)
	return installed, created, errors.Join(installErr, sourceRoot.Close())
}

// InstallFromRoot installs a slash-separated relative package path beneath an
// already trusted import root. The caller owns importRoot and must keep it open
// for the duration of the call.
func (m *Manager) InstallFromRoot(
	ctx context.Context,
	importRoot *os.Root,
	source string,
) (plugin.Installed, bool, error) {
	if err := m.beginOperation(); err != nil {
		return plugin.Installed{}, false, err
	}
	defer m.endOperation()
	if err := ctx.Err(); err != nil {
		return plugin.Installed{}, false, err
	}
	if importRoot == nil {
		return plugin.Installed{}, false, errors.New("pluginmanager: import root is required")
	}
	native, err := importRelativePath(source)
	if err != nil {
		return plugin.Installed{}, false, err
	}
	sourceRoot, err := openRootDirectoryWithoutSymlinks(importRoot, native)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("%w: validating source: %v", ErrInvalidSource, err)
	}
	installed, created, installErr := m.install(ctx, sourceRoot, sourceRoot.Name(), true)
	return installed, created, errors.Join(installErr, sourceRoot.Close())
}

func (m *Manager) install(
	ctx context.Context,
	sourceRoot *os.Root,
	source string,
	untrusted bool,
) (plugin.Installed, bool, error) {
	if err := m.requireStableStorage(); err != nil {
		return plugin.Installed{}, false, err
	}
	overlap, err := m.sourceOverlapsStorage(sourceRoot, source)
	if err != nil {
		return plugin.Installed{}, false, sourcePhaseError(untrusted, "checking package location", err)
	}
	if overlap {
		return plugin.Installed{}, false, fmt.Errorf("%w: source and storage root must not overlap", ErrInvalidSource)
	}
	manifest, _, err := plugin.LoadManifestRoot(sourceRoot)
	if err != nil {
		return plugin.Installed{}, false, sourcePhaseError(untrusted, "loading manifest", err)
	}
	canonicalManifest, err := json.Marshal(manifest)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: canonicalizing manifest: %w", err)
	}
	digest, err := plugin.VerifyPackageRoot(sourceRoot, manifest)
	if err != nil {
		return plugin.Installed{}, false, sourcePhaseError(untrusted, "verifying package", err)
	}
	if existing, err := m.repo.GetPlugin(ctx, manifest.ID); err == nil {
		if existing.Version != manifest.Version || existing.Digest != digest {
			return plugin.Installed{}, false, plugin.ErrConflict
		}
		if verifyErr := m.verifyInstalled(existing); verifyErr != nil {
			return plugin.Installed{}, false, fmt.Errorf("pluginmanager: installed package is corrupt: %w", verifyErr)
		}
		return existing, false, nil
	} else if !errors.Is(err, plugin.ErrNotFound) {
		return plugin.Installed{}, false, err
	}

	parentRelative := filepath.Join(manifest.ID, manifest.Version)
	destinationName := strings.TrimPrefix(digest, "sha256:")
	destinationRelative := filepath.Join(parentRelative, destinationName)
	destination := filepath.Join(m.root, destinationRelative)
	if err := mkdirAllWithoutSymlinks(m.storage, parentRelative, 0o700); err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: creating package parent: %w", err)
	}
	parentRoot, err := openRootDirectoryWithoutSymlinks(m.storage, parentRelative)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: opening package parent: %w", err)
	}
	defer parentRoot.Close()
	stagingName, err := makeTempDirectory(parentRoot, ".install-")
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: creating staging directory: %w", err)
	}
	defer parentRoot.RemoveAll(stagingName)
	stagingRoot, err := parentRoot.OpenRoot(stagingName)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: opening staging directory: %w", err)
	}
	if err := copyPackage(ctx, sourceRoot, stagingRoot); err != nil {
		return plugin.Installed{}, false, errors.Join(err, stagingRoot.Close())
	}
	copiedManifest, copiedCanonical, loadErr := loadCanonicalManifest(stagingRoot)
	if loadErr == nil && (!reflect.DeepEqual(copiedManifest, manifest) || !bytes.Equal(copiedCanonical, canonicalManifest)) {
		loadErr = errors.New("pluginmanager: manifest changed while copying")
	}
	copiedDigest, verifyErr := plugin.VerifyPackageRoot(stagingRoot, copiedManifest)
	closeErr := stagingRoot.Close()
	if loadErr != nil || verifyErr != nil || closeErr != nil || copiedDigest != digest {
		return plugin.Installed{}, false, sourcePhaseError(
			untrusted,
			"checking copied package",
			errors.Join(plugin.ErrIntegrity, loadErr, verifyErr, closeErr),
		)
	}
	createdFiles := false
	if info, err := parentRoot.Lstat(destinationName); errors.Is(err, os.ErrNotExist) {
		if err := parentRoot.Rename(stagingName, destinationName); err != nil {
			return plugin.Installed{}, false, fmt.Errorf("pluginmanager: publishing package: %w", err)
		}
		createdFiles = true
	} else if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: checking destination: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return plugin.Installed{}, false, errors.New("pluginmanager: destination is not a regular package directory")
	}
	publishedRoot, err := openRootDirectoryWithoutSymlinks(parentRoot, destinationName)
	if err != nil {
		if createdFiles {
			_ = parentRoot.RemoveAll(destinationName)
		}
		return plugin.Installed{}, false, fmt.Errorf("pluginmanager: opening published package: %w", err)
	}
	publishedManifest, publishedCanonical, loadErr := loadCanonicalManifest(publishedRoot)
	if loadErr == nil && (!reflect.DeepEqual(publishedManifest, manifest) || !bytes.Equal(publishedCanonical, canonicalManifest)) {
		loadErr = errors.New("pluginmanager: published manifest differs from verified manifest")
	}
	publishedDigest, verifyErr := plugin.VerifyPackageRoot(publishedRoot, publishedManifest)
	closeErr = publishedRoot.Close()
	if loadErr != nil || verifyErr != nil || closeErr != nil || publishedDigest != digest {
		if createdFiles {
			_ = parentRoot.RemoveAll(destinationName)
		}
		return plugin.Installed{}, false, fmt.Errorf(
			"pluginmanager: verifying published package: %w",
			errors.Join(plugin.ErrIntegrity, loadErr, verifyErr, closeErr),
		)
	}
	if err := m.requireStableStorage(); err != nil {
		if createdFiles {
			_ = parentRoot.RemoveAll(destinationName)
		}
		return plugin.Installed{}, false, err
	}
	now := time.Now().UTC()
	item := plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID,
		Name:          manifest.Name,
		Description:   manifest.Description,
		Version:       manifest.Version,
		Source:        source,
		Digest:        digest,
		Enabled:       false,
		TrustStatus:   "local-unverified",
		Manifest:      manifest,
		InstalledAt:   now,
		UpdatedAt:     now,
		InstallPath:   destination,
	}
	installed, created, err := m.repo.InstallPlugin(ctx, item)
	if err != nil {
		if createdFiles {
			_ = parentRoot.RemoveAll(destinationName)
		}
		return plugin.Installed{}, false, err
	}
	return installed, created, nil
}

func importRelativePath(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" || len(source) > 4_096 || strings.ContainsAny(source, `\:`) ||
		strings.ContainsRune(source, 0) || path.IsAbs(source) || path.Clean(source) != source {
		return "", fmt.Errorf("%w: source must be a safe relative path beneath the import root", ErrInvalidSource)
	}
	native := filepath.FromSlash(source)
	if !filepath.IsLocal(native) {
		return "", fmt.Errorf("%w: source must be a safe relative path beneath the import root", ErrInvalidSource)
	}
	return native, nil
}

func pathsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func (m *Manager) requireStableStorage() error {
	current, err := os.Stat(m.root)
	if err != nil {
		return fmt.Errorf("pluginmanager: storage root is unavailable: %w", err)
	}
	opened, err := m.storage.Stat(".")
	if err != nil {
		return fmt.Errorf("pluginmanager: storage root handle is unavailable: %w", err)
	}
	if !os.SameFile(m.rootInfo, opened) || !os.SameFile(opened, current) {
		return errors.New("pluginmanager: storage root changed on disk")
	}
	return nil
}

func (m *Manager) sourceOverlapsStorage(sourceRoot *os.Root, source string) (bool, error) {
	sourceInfo, err := sourceRoot.Stat(".")
	if err != nil {
		return false, err
	}
	storageInfo, err := m.storage.Stat(".")
	if err != nil {
		return false, err
	}
	if os.SameFile(sourceInfo, storageInfo) {
		return true, nil
	}
	return pathsOverlap(source, m.root) ||
		ancestorHasIdentity(source, storageInfo) ||
		ancestorHasIdentity(m.root, sourceInfo), nil
}

func ancestorHasIdentity(start string, target fs.FileInfo) bool {
	for current := filepath.Clean(start); ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && os.SameFile(info, target) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}

func sourcePhaseError(untrusted bool, phase string, err error) error {
	if err == nil || !untrusted || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalidSource, phase, err)
}

func loadCanonicalManifest(root *os.Root) (plugin.Manifest, []byte, error) {
	manifest, _, err := plugin.LoadManifestRoot(root)
	if err != nil {
		return plugin.Manifest{}, nil, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return plugin.Manifest{}, nil, err
	}
	return manifest, canonical, nil
}

func mkdirAllWithoutSymlinks(root *os.Root, relative string, mode fs.FileMode) error {
	if !filepath.IsLocal(relative) || strings.ContainsRune(relative, 0) {
		return fmt.Errorf("pluginmanager: path is not local: %q", relative)
	}
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, mode); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("pluginmanager: package parent is not a regular directory: %s", current)
		}
	}
	return nil
}

func makeTempDirectory(root *os.Root, prefix string) (string, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := root.Mkdir(name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("pluginmanager: could not allocate temporary directory")
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func rootInfoWithoutSymlinks(root *os.Root, relative string) (fs.FileInfo, error) {
	if root == nil {
		return nil, errors.New("pluginmanager: root is required")
	}
	if relative == "" || !filepath.IsLocal(relative) || strings.ContainsRune(relative, 0) {
		return nil, fmt.Errorf("pluginmanager: path is not local: %q", relative)
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	current := ""
	var info fs.FileInfo
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		var err error
		info, err = root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("pluginmanager: symbolic links are not allowed: %s", current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("pluginmanager: non-directory path component: %s", current)
		}
	}
	return info, nil
}

func openRootDirectoryWithoutSymlinks(root *os.Root, relative string) (*os.Root, error) {
	info, err := rootInfoWithoutSymlinks(root, relative)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("pluginmanager: source must be a directory")
	}
	directory, err := root.OpenRoot(relative)
	if err != nil {
		return nil, err
	}
	opened, statErr := directory.Stat(".")
	current, currentErr := rootInfoWithoutSymlinks(root, relative)
	if err := errors.Join(statErr, currentErr); err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	if !opened.IsDir() || !current.IsDir() || !os.SameFile(info, opened) || !os.SameFile(opened, current) {
		return nil, errors.Join(
			fmt.Errorf("pluginmanager: source directory changed while opening: %s", relative),
			directory.Close(),
		)
	}
	return directory, nil
}

func copyPackage(ctx context.Context, source, destination *os.Root) error {
	if source == nil || destination == nil {
		return errors.New("pluginmanager: source and destination roots are required")
	}
	var payloadFiles int
	var payloadBytes int64
	return fs.WalkDir(source.FS(), ".", func(sourcePath string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("%w: walking package: %v", ErrInvalidSource, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sourcePath == "." {
			return nil
		}
		native := filepath.FromSlash(sourcePath)
		if !filepath.IsLocal(native) || strings.ContainsRune(native, 0) {
			return fmt.Errorf("%w: package path is not local: %q", ErrInvalidSource, sourcePath)
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not allowed: %s", ErrInvalidSource, sourcePath)
		}
		if item.IsDir() {
			return mkdirAllWithoutSymlinks(destination, native, 0o700)
		}
		if !item.Type().IsRegular() {
			return fmt.Errorf("%w: non-regular file is not allowed: %s", ErrInvalidSource, sourcePath)
		}
		info, err := rootInfoWithoutSymlinks(source, native)
		if err != nil {
			return fmt.Errorf("%w: inspecting package file %q: %v", ErrInvalidSource, sourcePath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular file is not allowed: %s", ErrInvalidSource, sourcePath)
		}
		input, err := source.Open(native)
		if err != nil {
			return fmt.Errorf("%w: opening package file %q: %v", ErrInvalidSource, sourcePath, err)
		}
		opened, statErr := input.Stat()
		current, lstatErr := source.Lstat(native)
		if err := errors.Join(statErr, lstatErr); err != nil {
			return errors.Join(
				fmt.Errorf("%w: inspecting opened package file %q: %v", ErrInvalidSource, sourcePath, err),
				input.Close(),
			)
		}
		if current.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() ||
			!current.Mode().IsRegular() || !os.SameFile(info, opened) || !os.SameFile(opened, current) {
			return errors.Join(
				fmt.Errorf("%w: package file changed while opening: %s", ErrInvalidSource, sourcePath),
				input.Close(),
			)
		}
		limit := int64(plugin.MaxManifestBytes)
		if sourcePath != plugin.ManifestFile {
			if payloadFiles >= plugin.MaxPackageFiles {
				return errors.Join(fmt.Errorf("%w: package exceeds file limit", ErrInvalidSource), input.Close())
			}
			payloadFiles++
			limit = int64(plugin.MaxPackageBytes) - payloadBytes
		}
		if parent := filepath.Dir(native); parent != "." {
			parentInfo, err := rootInfoWithoutSymlinks(destination, parent)
			if err != nil {
				return errors.Join(err, input.Close())
			}
			if !parentInfo.IsDir() {
				return errors.Join(
					fmt.Errorf("pluginmanager: destination parent is not a directory: %s", parent),
					input.Close(),
				)
			}
		}
		output, err := destination.OpenFile(native, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return errors.Join(err, input.Close())
		}
		copied, copyErr := io.Copy(output, &contextReader{
			ctx:    ctx,
			reader: io.LimitReader(input, limit+1),
		})
		if copyErr == nil && copied > limit {
			copyErr = fmt.Errorf("%w: package exceeds file or byte limit", ErrInvalidSource)
		}
		if sourcePath != plugin.ManifestFile {
			payloadBytes += copied
		}
		return errors.Join(copyErr, output.Sync(), output.Close(), input.Close())
	})
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func (m *Manager) Get(ctx context.Context, pluginID string) (plugin.Installed, error) {
	if err := m.beginOperation(); err != nil {
		return plugin.Installed{}, err
	}
	defer m.endOperation()
	return m.repo.GetPlugin(ctx, pluginID)
}

func (m *Manager) List(ctx context.Context) ([]plugin.Installed, error) {
	if err := m.beginOperation(); err != nil {
		return nil, err
	}
	defer m.endOperation()
	return m.repo.ListPlugins(ctx)
}

func (m *Manager) Enable(ctx context.Context, pluginID string) (plugin.Installed, error) {
	if err := m.beginOperation(); err != nil {
		return plugin.Installed{}, err
	}
	defer m.endOperation()
	item, err := m.repo.GetPlugin(ctx, pluginID)
	if err != nil {
		return plugin.Installed{}, err
	}
	if err := m.verifyInstalled(item); err != nil {
		return plugin.Installed{}, fmt.Errorf("pluginmanager: refusing to enable corrupt package: %w", err)
	}
	return m.repo.SetPluginEnabled(ctx, pluginID, true)
}

func (m *Manager) Disable(ctx context.Context, pluginID string) (plugin.Installed, error) {
	if err := m.beginOperation(); err != nil {
		return plugin.Installed{}, err
	}
	defer m.endOperation()
	return m.repo.SetPluginEnabled(ctx, pluginID, false)
}

// Remove quarantines exact package bytes before deleting the durable record.
func (m *Manager) Remove(ctx context.Context, pluginID string) error {
	if err := m.beginOperation(); err != nil {
		return err
	}
	defer m.endOperation()
	item, err := m.repo.GetPlugin(ctx, pluginID)
	if err != nil {
		return err
	}
	if err := m.requireStableStorage(); err != nil {
		return err
	}
	relative, err := m.installedRelative(item)
	if err != nil {
		return err
	}
	parentRelative, packageName := filepath.Split(relative)
	parentRelative = filepath.Clean(parentRelative)
	parentRoot, err := openRootDirectoryWithoutSymlinks(m.storage, parentRelative)
	if err != nil {
		return fmt.Errorf("pluginmanager: opening package parent: %w", err)
	}
	defer parentRoot.Close()
	quarantine, err := reserveTempName(parentRoot, ".remove-")
	if err != nil {
		return fmt.Errorf("pluginmanager: preparing removal quarantine: %w", err)
	}
	if err := parentRoot.Rename(packageName, quarantine); err != nil {
		return fmt.Errorf("pluginmanager: quarantining package: %w", err)
	}
	if err := m.repo.DeletePlugin(ctx, pluginID); err != nil {
		rollbackErr := parentRoot.Rename(quarantine, packageName)
		return errors.Join(err, rollbackErr)
	}
	if err := parentRoot.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("pluginmanager: deleting quarantined package: %w", err)
	}
	removeEmptyParentsRoot(m.storage, parentRelative)
	return nil
}

func (m *Manager) verifyInstalled(item plugin.Installed) error {
	if err := m.requireStableStorage(); err != nil {
		return err
	}
	relative, err := m.installedRelative(item)
	if err != nil {
		return err
	}
	root, err := openRootDirectoryWithoutSymlinks(m.storage, relative)
	if err != nil {
		return err
	}
	defer root.Close()
	manifest, canonical, err := loadCanonicalManifest(root)
	if err != nil {
		return err
	}
	wantCanonical, err := json.Marshal(item.Manifest)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest, item.Manifest) || !bytes.Equal(canonical, wantCanonical) {
		return errors.New("pluginmanager: installed manifest differs from durable manifest")
	}
	digest, err := plugin.VerifyPackageRoot(root, manifest)
	if err != nil {
		return err
	}
	if digest != item.Digest {
		return plugin.ErrIntegrity
	}
	return nil
}

func (m *Manager) installedRelative(item plugin.Installed) (string, error) {
	if item.ID != item.Manifest.ID || item.Version != item.Manifest.Version ||
		item.Digest != item.Manifest.Integrity.Files || !strings.HasPrefix(item.Digest, "sha256:") {
		return "", errors.New("pluginmanager: inconsistent installed package metadata")
	}
	relative := filepath.Join(item.ID, item.Version, strings.TrimPrefix(item.Digest, "sha256:"))
	if !filepath.IsLocal(relative) || filepath.Clean(item.InstallPath) != filepath.Join(m.root, relative) {
		return "", errors.New("pluginmanager: refusing path outside plugin root")
	}
	return relative, nil
}

func reserveTempName(root *os.Root, prefix string) (string, error) {
	name, err := makeTempDirectory(root, prefix)
	if err != nil {
		return "", err
	}
	if err := root.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

func removeEmptyParentsRoot(root *os.Root, directory string) {
	for directory != "." {
		if err := root.Remove(directory); err != nil {
			return
		}
		directory = filepath.Dir(directory)
	}
}
