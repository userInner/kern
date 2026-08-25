// Package workspace provides bounded, root-confined filesystem access.
package workspace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	defaultMaxReadBytes = 2 << 20
	defaultMaxEntries   = 500
	defaultSearchFiles  = 2_000
	defaultSearchBytes  = 8 << 20
	defaultSearchHits   = 200
	maxDiffInputBytes   = 512 << 10
	maxDiffLines        = 12_000
	maxDiffOutputBytes  = 128 << 10
)

var (
	ErrOutsideRoot  = errors.New("workspace: path is outside root")
	ErrSensitive    = errors.New("workspace: sensitive path is blocked")
	ErrNotRegular   = errors.New("workspace: path is not a regular file")
	ErrTooLarge     = errors.New("workspace: content exceeds size limit")
	ErrHashMismatch = errors.New("workspace: content hash mismatch")
)

// File contains bounded file content and integrity metadata.
type File struct {
	Path   string `json:"path"`
	Data   []byte `json:"-"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Entry is one bounded directory entry.
type Entry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// Match is one text-search result.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Change records atomic write integrity metadata.
type Change struct {
	Path         string `json:"path"`
	MovedFrom    string `json:"moved_from,omitempty"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256"`
	Created      bool   `json:"created"`
	Bytes        int64  `json:"bytes"`
	Diff         string `json:"diff,omitempty"`
	DiffStatus   string `json:"diff_status"`
}

// WriteIntent is the durable, content-free invariant needed to reconcile an
// atomic file write after a runtime interruption.
type WriteIntent struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256"`
	Created      bool   `json:"created"`
}

// MoveIntent is the content-free invariant used to reconcile an interrupted
// workspace move without replaying it blindly.
type MoveIntent struct {
	SourcePath      string `json:"source_path"`
	DestinationPath string `json:"destination_path"`
	SHA256          string `json:"sha256"`
}

// Workspace confines filesystem operations to one root directory.
type Workspace struct {
	root         *os.Root
	maxReadBytes int64
}

// Open opens a filesystem root for bounded concurrent use.
func Open(path string) (*Workspace, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("opening workspace root: %w", err)
	}
	return &Workspace{root: root, maxReadBytes: defaultMaxReadBytes}, nil
}

// Close releases the root handle.
func (w *Workspace) Close() error {
	return w.root.Close()
}

// Root returns the display path of this workspace.
func (w *Workspace) Root() string {
	return w.root.Name()
}

// ResolveDir returns an operating-system directory path after root confinement checks.
func (w *Workspace) ResolveDir(path string) (string, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return "", err
	}
	if err := allowPath(clean); err != nil {
		return "", err
	}
	subroot, err := w.root.OpenRoot(clean)
	if err != nil {
		return "", fmt.Errorf("opening workspace subdirectory: %w", err)
	}
	defer subroot.Close()
	return subroot.Name(), nil
}

// ReadFile reads one regular non-sensitive file up to the configured limit.
func (w *Workspace) ReadFile(ctx context.Context, path string) (File, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return File{}, err
	}
	if err := allowPath(clean); err != nil {
		return File{}, err
	}
	handle, err := w.root.Open(clean)
	if err != nil {
		return File{}, fmt.Errorf("opening workspace file: %w", err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return File{}, fmt.Errorf("stating workspace file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return File{}, ErrNotRegular
	}
	if info.Size() > w.maxReadBytes {
		return File{}, ErrTooLarge
	}
	data, err := readAllContext(ctx, io.LimitReader(handle, w.maxReadBytes+1))
	if err != nil {
		return File{}, fmt.Errorf("reading workspace file: %w", err)
	}
	if int64(len(data)) > w.maxReadBytes {
		return File{}, ErrTooLarge
	}
	digest := sha256.Sum256(data)
	return File{
		Path:   filepath.ToSlash(clean),
		Data:   data,
		SHA256: hex.EncodeToString(digest[:]),
		Size:   int64(len(data)),
	}, nil
}

// ListDir returns a deterministic bounded directory listing.
func (w *Workspace) ListDir(ctx context.Context, path string) ([]Entry, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	if err := allowPath(clean); err != nil {
		return nil, err
	}
	handle, err := w.root.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("opening workspace directory: %w", err)
	}
	defer handle.Close()
	entries, err := handle.ReadDir(defaultMaxEntries + 1)
	if err != nil {
		return nil, fmt.Errorf("reading workspace directory: %w", err)
	}
	if len(entries) > defaultMaxEntries {
		return nil, ErrTooLarge
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entryPath := filepath.Join(clean, entry.Name())
		if allowPath(entryPath) != nil {
			continue
		}
		item := Entry{Name: entry.Name(), IsDir: entry.IsDir()}
		if !entry.IsDir() {
			if info, err := entry.Info(); err == nil {
				item.Size = info.Size()
			}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// Search finds a bounded number of literal text matches beneath path.
func (w *Workspace) Search(ctx context.Context, path, query string) ([]Match, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	if err := allowPath(clean); err != nil {
		return nil, err
	}
	if query == "" {
		return nil, errors.New("workspace: search query is required")
	}
	var files int
	var bytesRead int64
	matches := make([]Match, 0)
	err = fs.WalkDir(w.root.FS(), clean, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != clean && entry.IsDir() && shouldSkipDirectory(entry.Name()) {
			return fs.SkipDir
		}
		if allowPath(path) != nil {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		files++
		if files > defaultSearchFiles {
			return ErrTooLarge
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > w.maxReadBytes {
			return nil
		}
		bytesRead += info.Size()
		if bytesRead > defaultSearchBytes {
			return ErrTooLarge
		}
		handle, err := w.root.Open(path)
		if err != nil {
			return err
		}
		defer handle.Close()
		scanner := bufio.NewScanner(handle)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for line := 1; scanner.Scan(); line++ {
			text := scanner.Text()
			if strings.IndexByte(text, 0) >= 0 {
				return nil
			}
			if strings.Contains(text, query) {
				matches = append(matches, Match{
					Path: filepath.ToSlash(path),
					Line: line,
					Text: truncate(text, 500),
				})
				if len(matches) >= defaultSearchHits {
					return fs.SkipAll
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scanning %s: %w", path, err)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, fmt.Errorf("searching workspace: %w", err)
	}
	return matches, nil
}

// WriteFile atomically creates or replaces a file after hash verification.
func (w *Workspace) WriteFile(
	ctx context.Context,
	path string,
	data []byte,
	expectedSHA256 string,
) (Change, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return Change{}, err
	}
	if clean == "." {
		return Change{}, ErrNotRegular
	}
	if err := allowPath(clean); err != nil {
		return Change{}, err
	}
	if int64(len(data)) > w.maxReadBytes {
		return Change{}, ErrTooLarge
	}
	beforeHash := ""
	var beforeData []byte
	created := false
	mode := fs.FileMode(0o644)
	before, err := w.ReadFile(ctx, clean)
	if err == nil {
		beforeHash = before.SHA256
		beforeData = before.Data
		info, statErr := w.root.Stat(clean)
		if statErr != nil {
			return Change{}, fmt.Errorf("stating existing file: %w", statErr)
		}
		mode = info.Mode().Perm()
		if expectedSHA256 == "" || !strings.EqualFold(expectedSHA256, beforeHash) {
			return Change{}, ErrHashMismatch
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		created = true
		if expectedSHA256 != "" {
			return Change{}, ErrHashMismatch
		}
	} else {
		return Change{}, err
	}
	if err := ctx.Err(); err != nil {
		return Change{}, err
	}
	parent := filepath.Dir(clean)
	if parent != "." {
		if err := w.root.MkdirAll(parent, 0o755); err != nil {
			return Change{}, fmt.Errorf("creating parent directory: %w", err)
		}
	}
	temporary, err := tempName(parent)
	if err != nil {
		return Change{}, err
	}
	handle, err := w.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Change{}, fmt.Errorf("creating temporary file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = w.root.Remove(temporary)
		}
	}()
	if _, err := handle.Write(data); err != nil {
		_ = handle.Close()
		return Change{}, fmt.Errorf("writing temporary file: %w", err)
	}
	if err := handle.Chmod(mode); err != nil {
		_ = handle.Close()
		return Change{}, fmt.Errorf("setting temporary file mode: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return Change{}, fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := handle.Close(); err != nil {
		return Change{}, fmt.Errorf("closing temporary file: %w", err)
	}
	if err := w.root.Rename(temporary, clean); err != nil {
		return Change{}, fmt.Errorf("replacing workspace file: %w", err)
	}
	removeTemporary = false
	if directory, err := w.root.Open(parent); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	afterDigest := sha256.Sum256(data)
	diff, diffStatus := unifiedDiff(clean, beforeData, data)
	return Change{
		Path:         filepath.ToSlash(clean),
		BeforeSHA256: beforeHash,
		AfterSHA256:  hex.EncodeToString(afterDigest[:]),
		Created:      created,
		Bytes:        int64(len(data)),
		Diff:         diff,
		DiffStatus:   diffStatus,
	}, nil
}

// PrepareWrite validates the same preconditions as WriteFile and computes its
// target hash without changing the workspace.
func (w *Workspace) PrepareWrite(
	ctx context.Context,
	path string,
	data []byte,
	expectedSHA256 string,
) (WriteIntent, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return WriteIntent{}, err
	}
	if clean == "." {
		return WriteIntent{}, ErrNotRegular
	}
	if err := allowPath(clean); err != nil {
		return WriteIntent{}, err
	}
	if int64(len(data)) > w.maxReadBytes {
		return WriteIntent{}, ErrTooLarge
	}
	intent := WriteIntent{Path: filepath.ToSlash(clean)}
	before, err := w.ReadFile(ctx, clean)
	if err == nil {
		if expectedSHA256 == "" || !strings.EqualFold(expectedSHA256, before.SHA256) {
			return WriteIntent{}, ErrHashMismatch
		}
		intent.BeforeSHA256 = before.SHA256
	} else if errors.Is(err, fs.ErrNotExist) {
		if expectedSHA256 != "" {
			return WriteIntent{}, ErrHashMismatch
		}
		intent.Created = true
	} else {
		return WriteIntent{}, err
	}
	if err := ctx.Err(); err != nil {
		return WriteIntent{}, err
	}
	after := sha256.Sum256(data)
	intent.AfterSHA256 = hex.EncodeToString(after[:])
	return intent, nil
}

func unifiedDiff(path string, before, after []byte) (string, string) {
	if bytes.Equal(before, after) {
		return "", "unchanged"
	}
	if bytes.IndexByte(before, 0) >= 0 || bytes.IndexByte(after, 0) >= 0 {
		return "", "binary"
	}
	if len(before) > maxDiffInputBytes || len(after) > maxDiffInputBytes ||
		bytes.Count(before, []byte{'\n'})+bytes.Count(after, []byte{'\n'}) > maxDiffLines {
		return "", "too_large"
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
		FromFile: "a/" + filepath.ToSlash(path),
		ToFile:   "b/" + filepath.ToSlash(path),
		Context:  3,
	})
	if err != nil {
		return "", "unavailable"
	}
	if len(diff) <= maxDiffOutputBytes {
		return diff, "complete"
	}
	limit := maxDiffOutputBytes
	for limit > 0 && !utf8.ValidString(diff[:limit]) {
		limit--
	}
	return diff[:limit] + "\n... diff truncated by Kern ...\n", "truncated"
}

// Replace replaces exactly one literal occurrence and writes atomically.
func (w *Workspace) Replace(
	ctx context.Context,
	path string,
	old string,
	newText string,
	expectedSHA256 string,
) (Change, error) {
	if old == "" {
		return Change{}, errors.New("workspace: replacement source is required")
	}
	file, err := w.ReadFile(ctx, path)
	if err != nil {
		return Change{}, err
	}
	if expectedSHA256 == "" || !strings.EqualFold(expectedSHA256, file.SHA256) {
		return Change{}, ErrHashMismatch
	}
	if count := bytesCount(file.Data, []byte(old)); count != 1 {
		return Change{}, fmt.Errorf("workspace: replacement matched %d times", count)
	}
	updated := strings.Replace(string(file.Data), old, newText, 1)
	return w.WriteFile(ctx, path, []byte(updated), expectedSHA256)
}

// PrepareMove validates a non-overwriting file move without changing either
// path. The returned hash is persisted before Rename starts.
func (w *Workspace) PrepareMove(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	expectedSHA256 string,
) (MoveIntent, error) {
	source, destination, file, err := w.validateMove(
		ctx,
		sourcePath,
		destinationPath,
		expectedSHA256,
	)
	if err != nil {
		return MoveIntent{}, err
	}
	return MoveIntent{
		SourcePath:      filepath.ToSlash(source),
		DestinationPath: filepath.ToSlash(destination),
		SHA256:          file.SHA256,
	}, nil
}

// MoveFile moves one regular file inside the workspace without intentionally
// replacing an existing destination. The source hash prevents stale moves.
func (w *Workspace) MoveFile(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	expectedSHA256 string,
) (Change, error) {
	source, destination, file, err := w.validateMove(
		ctx,
		sourcePath,
		destinationPath,
		expectedSHA256,
	)
	if err != nil {
		return Change{}, err
	}
	if err := w.root.Rename(source, destination); err != nil {
		return Change{}, fmt.Errorf("moving workspace file: %w", err)
	}
	sourceDisplay := filepath.ToSlash(source)
	destinationDisplay := filepath.ToSlash(destination)
	return Change{
		Path:         destinationDisplay,
		MovedFrom:    sourceDisplay,
		BeforeSHA256: file.SHA256,
		AfterSHA256:  file.SHA256,
		Created:      true,
		Bytes:        file.Size,
		Diff: fmt.Sprintf(
			"similarity index 100%%\nrename from %q\nrename to %q\n",
			sourceDisplay,
			destinationDisplay,
		),
		DiffStatus: "complete",
	}, nil
}

func (w *Workspace) validateMove(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	expectedSHA256 string,
) (string, string, File, error) {
	source, err := cleanPath(sourcePath)
	if err != nil {
		return "", "", File{}, err
	}
	destination, err := cleanPath(destinationPath)
	if err != nil {
		return "", "", File{}, err
	}
	if source == destination || destination == "." {
		return "", "", File{}, errors.New("workspace: move destination must be a different file path")
	}
	if err := allowPath(source); err != nil {
		return "", "", File{}, err
	}
	if err := allowPath(destination); err != nil {
		return "", "", File{}, err
	}
	if expectedSHA256 == "" {
		return "", "", File{}, ErrHashMismatch
	}
	file, err := w.ReadFile(ctx, source)
	if err != nil {
		return "", "", File{}, err
	}
	info, err := w.root.Lstat(source)
	if err != nil {
		return "", "", File{}, fmt.Errorf("stating move source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", File{}, ErrNotRegular
	}
	if !strings.EqualFold(expectedSHA256, file.SHA256) {
		return "", "", File{}, ErrHashMismatch
	}
	if _, err := w.root.Stat(destination); err == nil {
		return "", "", File{}, fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", File{}, fmt.Errorf("checking move destination: %w", err)
	}
	return source, destination, file, nil
}

// PrepareReplace validates and computes the exact target hash without writing.
func (w *Workspace) PrepareReplace(
	ctx context.Context,
	path string,
	old string,
	newText string,
	expectedSHA256 string,
) (WriteIntent, error) {
	if old == "" {
		return WriteIntent{}, errors.New("workspace: replacement source is required")
	}
	file, err := w.ReadFile(ctx, path)
	if err != nil {
		return WriteIntent{}, err
	}
	if expectedSHA256 == "" || !strings.EqualFold(expectedSHA256, file.SHA256) {
		return WriteIntent{}, ErrHashMismatch
	}
	if count := bytesCount(file.Data, []byte(old)); count != 1 {
		return WriteIntent{}, fmt.Errorf("workspace: replacement matched %d times", count)
	}
	updated := strings.Replace(string(file.Data), old, newText, 1)
	return w.PrepareWrite(ctx, path, []byte(updated), expectedSHA256)
}

func cleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	if strings.ContainsRune(path, 0) || !filepath.IsLocal(path) {
		return "", ErrOutsideRoot
	}
	return filepath.Clean(path), nil
}

func allowPath(path string) error {
	for _, segment := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' }) {
		lower := strings.ToLower(segment)
		if lower == ".git" || lower == ".ssh" || lower == ".aws" || lower == ".gnupg" ||
			lower == ".env" || strings.HasPrefix(lower, ".env.") ||
			strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
			return ErrSensitive
		}
	}
	return nil
}

func shouldSkipDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".kern", "node_modules", "vendor", "dist", "build":
		return true
	default:
		return false
	}
}

func tempName(parent string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generating temporary file name: %w", err)
	}
	return filepath.Join(parent, ".kern-tmp-"+hex.EncodeToString(random)), nil
}

func readAllContext(ctx context.Context, reader io.Reader) ([]byte, error) {
	var output []byte
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := reader.Read(buffer)
		output = append(output, buffer[:count]...)
		if errors.Is(err, io.EOF) {
			return output, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func bytesCount(data, separator []byte) int {
	return strings.Count(string(data), string(separator))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
