package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxPatchFiles = 10_000
	maxPatchBytes = int64(512 << 20)
)

func gradePatchRule(
	ctx context.Context,
	baselineDir string,
	baselineFiles map[string]string,
	workspaceDir string,
	grader Grader,
) GradeResult {
	result := GradeResult{
		GraderID: grader.ID, Status: GradePassed, Score: 1,
		ReasonCode: "patch_rule_passed", Details: make(map[string]any),
	}
	if baselineFiles == nil && strings.TrimSpace(baselineDir) == "" {
		result.Status = GradeError
		result.Score = 0
		result.ReasonCode = "patch_baseline_unavailable"
		result.Details["error"] = "evaluation baseline is unavailable"
		return result
	}
	baseline := baselineFiles
	if baseline == nil {
		var err error
		baseline, err = snapshotFiles(ctx, baselineDir)
		if err != nil {
			return patchInfrastructureError(result, "baseline", err)
		}
	}
	current, err := snapshotFiles(ctx, workspaceDir)
	if err != nil {
		return patchInfrastructureError(result, "workspace", err)
	}
	changed := changedPaths(baseline, current)
	violations := make([]string, 0)
	if len(changed) < grader.MinChangedFiles {
		violations = append(violations, fmt.Sprintf("changed files %d is below minimum %d", len(changed), grader.MinChangedFiles))
	}
	if grader.MaxChangedFiles > 0 && len(changed) > grader.MaxChangedFiles {
		violations = append(violations, fmt.Sprintf("changed files %d exceeds maximum %d", len(changed), grader.MaxChangedFiles))
	}
	for _, name := range changed {
		if len(grader.AllowedPaths) > 0 && !matchesAnyPath(grader.AllowedPaths, name) {
			violations = append(violations, name+" is outside allowed paths")
		}
		if matchesAnyPath(grader.ForbiddenPaths, name) {
			violations = append(violations, name+" matches a forbidden path")
		}
	}
	result.Evidence = []string{"workspace_diff:" + digestPaths(changed)}
	result.Details["changed_paths"] = changed
	result.Details["changed_files"] = len(changed)
	if len(violations) > 0 {
		result.Status = GradeFailed
		result.Score = 0
		result.ReasonCode = "patch_rule_failed"
		result.Details["violations"] = violations
	}
	return result
}

func snapshotFiles(ctx context.Context, root string) (map[string]string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootHandle, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	return snapshotRoot(ctx, rootHandle)
}

func snapshotRoot(ctx context.Context, root *os.Root) (map[string]string, error) {
	return snapshotRootWithLimits(ctx, root, maxPatchFiles, maxPatchBytes)
}

func snapshotRootWithLimits(
	ctx context.Context,
	root *os.Root,
	maxFiles int,
	maxBytes int64,
) (map[string]string, error) {
	if root == nil {
		return nil, errors.New("evaluation: snapshot root is unavailable")
	}
	result := make(map[string]string)
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("evaluation: patch snapshot rejects non-regular file %q", name)
		}
		if len(result) >= maxFiles {
			return errors.New("evaluation: patch snapshot file limit exceeded")
		}
		local := filepath.FromSlash(name)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("evaluation: patch snapshot path %q is not local", name)
		}
		hash := sha256.New()
		copied, _, err := readRootFile(ctx, root, local, maxBytes-total, hash)
		total += copied
		if errors.Is(err, errFileByteLimit) {
			return errors.New("evaluation: patch snapshot byte limit exceeded")
		}
		if err != nil {
			return err
		}
		result[name] = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	return result, err
}

func changedPaths(baseline, current map[string]string) []string {
	set := make(map[string]bool, len(baseline)+len(current))
	for name := range baseline {
		set[name] = true
	}
	for name := range current {
		set[name] = true
	}
	changed := make([]string, 0)
	for name := range set {
		if baseline[name] != current[name] {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

func matchesAnyPath(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if pattern == name {
			return true
		}
		candidate := pattern
		if strings.HasPrefix(candidate, "**/") {
			candidate = strings.TrimPrefix(candidate, "**/")
			if matched, _ := path.Match(candidate, path.Base(name)); matched {
				return true
			}
		}
		if matched, _ := path.Match(candidate, name); matched {
			return true
		}
	}
	return false
}

func digestPaths(paths []string) string {
	hash := sha256.Sum256([]byte(strings.Join(paths, "\x00")))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func patchInfrastructureError(result GradeResult, source string, err error) GradeResult {
	result.Status = GradeError
	result.Score = 0
	result.ReasonCode = "patch_snapshot_failed"
	result.Details["source"] = source
	result.Details["error"] = err.Error()
	return result
}
