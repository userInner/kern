// Package artifact stores immutable task outputs by content digest.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/userInner/kern/internal/id"
)

const (
	SchemaVersion   = "1"
	defaultMaxBytes = 64 << 20
)

var (
	ErrNotFound  = errors.New("artifact: not found")
	ErrForbidden = errors.New("artifact: does not belong to task")
	ErrCorrupt   = errors.New("artifact: content integrity check failed")
	ErrTooLarge  = errors.New("artifact: content exceeds size limit")
)

// Artifact is one task-owned reference to an immutable content object.
type Artifact struct {
	SchemaVersion     string    `json:"schema_version"`
	ID                string    `json:"id"`
	TaskID            string    `json:"task_id"`
	AttemptID         string    `json:"attempt_id"`
	Name              string    `json:"name"`
	Digest            string    `json:"digest"`
	MediaType         string    `json:"media_type"`
	Size              int64     `json:"size"`
	StoragePath       string    `json:"-"`
	SourceOperationID string    `json:"source_operation_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// Repository persists artifact references and their task events.
type Repository interface {
	SaveArtifact(ctx context.Context, item Artifact) error
	GetArtifact(ctx context.Context, artifactID string) (Artifact, error)
	ListArtifacts(ctx context.Context, taskID string) ([]Artifact, error)
}

type referenceChecker interface {
	ArtifactStoragePathReferenced(ctx context.Context, path string) (bool, error)
}

// Store owns immutable content files while Repository owns their references.
type Store struct {
	root     string
	repo     Repository
	maxBytes int64
}

// Open creates a content-addressed artifact store beneath root.
func Open(root string, repo Repository) (*Store, error) {
	if strings.TrimSpace(root) == "" || repo == nil {
		return nil, errors.New("artifact: root and repository are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("artifact: resolving root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(absolute, "sha256"), 0o700); err != nil {
		return nil, fmt.Errorf("artifact: creating root: %w", err)
	}
	return &Store{root: absolute, repo: repo, maxBytes: defaultMaxBytes}, nil
}

// Put streams content into the store and creates a task-owned reference.
func (s *Store) Put(
	ctx context.Context,
	taskID string,
	attemptID string,
	name string,
	mediaType string,
	sourceOperationID string,
	content io.Reader,
) (Artifact, error) {
	if taskID == "" || attemptID == "" || content == nil {
		return Artifact{}, errors.New("artifact: task, attempt, and content are required")
	}
	name, err := cleanName(name)
	if err != nil {
		return Artifact{}, err
	}
	mediaType, err = cleanMediaType(mediaType)
	if err != nil {
		return Artifact{}, err
	}

	temporary, err := os.CreateTemp(s.root, ".kern-artifact-*")
	if err != nil {
		return Artifact{}, fmt.Errorf("artifact: creating temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return Artifact{}, fmt.Errorf("artifact: restricting temporary file: %w", err)
	}
	hash := sha256.New()
	size, copyErr := copyContext(ctx, io.MultiWriter(temporary, hash), io.LimitReader(content, s.maxBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		return Artifact{}, errors.Join(copyErr, closeErr)
	}
	if size > s.maxBytes {
		return Artifact{}, ErrTooLarge
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	relativePath := filepath.Join("sha256", digest[:2], digest[2:4], digest)
	objectPath := filepath.Join(s.root, relativePath)
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o700); err != nil {
		return Artifact{}, fmt.Errorf("artifact: creating digest directory: %w", err)
	}
	if err := os.Rename(temporaryPath, objectPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Artifact{}, fmt.Errorf("artifact: publishing content: %w", err)
		}
	} else if err := os.Chmod(objectPath, 0o600); err != nil {
		return Artifact{}, fmt.Errorf("artifact: restricting content: %w", err)
	}

	artifactID, err := id.New()
	if err != nil {
		return Artifact{}, err
	}
	item := Artifact{
		SchemaVersion:     SchemaVersion,
		ID:                artifactID,
		TaskID:            taskID,
		AttemptID:         attemptID,
		Name:              name,
		Digest:            digest,
		MediaType:         mediaType,
		Size:              size,
		StoragePath:       filepath.ToSlash(relativePath),
		SourceOperationID: sourceOperationID,
		CreatedAt:         time.Now().UTC(),
	}
	if err := s.repo.SaveArtifact(ctx, item); err != nil {
		return Artifact{}, err
	}
	return item, nil
}

// List returns a task's artifact references in creation order.
func (s *Store) List(ctx context.Context, taskID string) ([]Artifact, error) {
	return s.repo.ListArtifacts(ctx, taskID)
}

// ReadContent returns integrity-checked bounded artifact bytes for internal
// verifiers. HTTP clients continue to use OpenContent for streaming downloads.
func (s *Store) ReadContent(
	ctx context.Context,
	taskID string,
	artifactID string,
	maxBytes int64,
) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > s.maxBytes {
		return nil, ErrTooLarge
	}
	_, file, err := s.OpenContent(ctx, taskID, artifactID)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading artifact content: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// OpenContent verifies and rewinds an artifact before returning its file.
func (s *Store) OpenContent(
	ctx context.Context,
	taskID string,
	artifactID string,
) (Artifact, *os.File, error) {
	item, err := s.repo.GetArtifact(ctx, artifactID)
	if err != nil {
		return Artifact{}, nil, err
	}
	if item.TaskID != taskID {
		return Artifact{}, nil, ErrForbidden
	}
	path := filepath.Join(s.root, filepath.FromSlash(item.StoragePath))
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Artifact{}, nil, ErrCorrupt
		}
		return Artifact{}, nil, fmt.Errorf("artifact: opening content: %w", err)
	}
	hash := sha256.New()
	size, err := copyContext(ctx, hash, file)
	if err != nil {
		file.Close()
		return Artifact{}, nil, err
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if size != item.Size || actualDigest != item.Digest {
		file.Close()
		return Artifact{}, nil, ErrCorrupt
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return Artifact{}, nil, fmt.Errorf("artifact: rewinding content: %w", err)
	}
	return item, file, nil
}

// PruneUnreferenced removes only candidate content objects that no longer have
// a database reference. Candidate paths are validated against the immutable
// sha256 layout before any filesystem mutation occurs.
func (s *Store) PruneUnreferenced(ctx context.Context, candidates []string) (int, int64, error) {
	checker, ok := s.repo.(referenceChecker)
	if !ok {
		return 0, 0, errors.New("artifact: repository cannot check content references")
	}
	seen := make(map[string]struct{}, len(candidates))
	var removed int
	var removedBytes int64
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return removed, removedBytes, err
		}
		clean, err := cleanStoragePath(candidate)
		if err != nil {
			return removed, removedBytes, err
		}
		if _, duplicate := seen[clean]; duplicate {
			continue
		}
		seen[clean] = struct{}{}
		referenced, err := checker.ArtifactStoragePathReferenced(ctx, filepath.ToSlash(clean))
		if err != nil {
			return removed, removedBytes, err
		}
		if referenced {
			continue
		}
		path := filepath.Join(s.root, clean)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, removedBytes, fmt.Errorf("artifact: stating unreferenced object: %w", err)
		}
		if !info.Mode().IsRegular() {
			return removed, removedBytes, errors.New("artifact: refusing to prune a non-regular content object")
		}
		if err := os.Remove(path); err != nil {
			return removed, removedBytes, fmt.Errorf("artifact: pruning unreferenced object: %w", err)
		}
		removed++
		removedBytes += info.Size()
	}
	return removed, removedBytes, nil
}

func cleanStoragePath(path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if len(parts) != 4 || parts[0] != "sha256" || len(parts[1]) != 2 || len(parts[2]) != 2 ||
		len(parts[3]) != sha256.Size*2 || parts[1] != parts[3][:2] || parts[2] != parts[3][2:4] {
		return "", errors.New("artifact: invalid content storage path")
	}
	if _, err := hex.DecodeString(parts[3]); err != nil {
		return "", errors.New("artifact: invalid content digest path")
	}
	return clean, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || name == "." || len([]rune(name)) > 255 {
		return "", errors.New("artifact: name must be one safe file name")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", errors.New("artifact: name contains a null byte")
	}
	return name, nil
}

func cleanMediaType(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "application/octet-stream", nil
	}
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil || parsed == "" {
		return "", errors.New("artifact: invalid media type")
	}
	return strings.ToLower(parsed), nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64<<10)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
