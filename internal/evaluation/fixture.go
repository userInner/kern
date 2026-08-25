package evaluation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxFixtureFiles = 20_000
	maxFixtureBytes = 1 << 30
)

func copyFixture(ctx context.Context, source, destination string) error {
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("evaluation: destination workspace already exists")
		}
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("evaluation: creating workspace: %w", err)
	}
	files := 0
	var bytesCopied int64
	return filepath.WalkDir(source, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, sourcePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("evaluation: fixture symlink %q is not allowed", relative)
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("evaluation: fixture special file %q is not allowed", relative)
		}
		files++
		bytesCopied += info.Size()
		if files > maxFixtureFiles || bytesCopied > maxFixtureBytes {
			return errors.New("evaluation: fixture exceeds file or byte limit")
		}
		return copyFixtureFile(sourcePath, target, info.Mode())
	})
}

func resetFixtureWorkspace(runRoot, destination string) error {
	root, err := filepath.Abs(runRoot)
	if err != nil {
		return fmt.Errorf("evaluation: resolving run root: %w", err)
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("evaluation: resolving workspace: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("evaluation: workspace is outside run root")
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("evaluation: clearing incomplete workspace: %w", err)
	}
	return nil
}

func copyFixtureFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	permissions := mode.Perm() & 0o700
	if permissions&0o600 == 0 {
		permissions |= 0o600
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}
