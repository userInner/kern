package evaluation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	maxFixtureFiles = 20_000
	maxFixtureBytes = 1 << 30
)

func copyFixture(ctx context.Context, source *os.Root, destination string) error {
	if source == nil {
		return errors.New("evaluation: fixture root is unavailable")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("evaluation: destination workspace already exists")
		}
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("evaluation: creating workspace: %w", err)
	}
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("evaluation: opening workspace root: %w", err)
	}
	defer destinationRoot.Close()
	return copyFixtureContents(ctx, source, destinationRoot)
}

func copyFixtureToRoot(ctx context.Context, source, destination *os.Root, relative string) error {
	if source == nil || destination == nil {
		return errors.New("evaluation: fixture root is unavailable")
	}
	if !filepath.IsLocal(relative) || relative == "." {
		return errors.New("evaluation: snapshot fixture path is not local")
	}
	if _, err := destination.Stat(relative); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("evaluation: snapshot fixture already exists")
		}
		return err
	}
	if err := destination.MkdirAll(relative, 0o700); err != nil {
		return fmt.Errorf("evaluation: creating snapshot fixture: %w", err)
	}
	fixtureRoot, err := destination.OpenRoot(relative)
	if err != nil {
		return fmt.Errorf("evaluation: opening snapshot fixture: %w", err)
	}
	defer fixtureRoot.Close()
	return copyFixtureContents(ctx, source, fixtureRoot)
}

func copyFixtureContents(ctx context.Context, source, destinationRoot *os.Root) error {
	return copyFixtureContentsWithLimits(ctx, source, destinationRoot, maxFixtureFiles, maxFixtureBytes)
}

func copyFixtureContentsWithLimits(
	ctx context.Context,
	source, destinationRoot *os.Root,
	maxFiles int,
	maxBytes int64,
) error {
	files := 0
	var bytesCopied int64
	return fs.WalkDir(source.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("evaluation: fixture symlink %q is not allowed", relative)
		}
		local := filepath.FromSlash(relative)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("evaluation: fixture path %q is not local", relative)
		}
		if entry.IsDir() {
			return destinationRoot.Mkdir(local, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("evaluation: fixture special file %q is not allowed", relative)
		}
		files++
		if files > maxFiles {
			return errors.New("evaluation: fixture exceeds file or byte limit")
		}
		copied, err := copyFixtureFile(ctx, source, destinationRoot, local, maxBytes-bytesCopied)
		bytesCopied += copied
		if errors.Is(err, errFileByteLimit) {
			return errors.New("evaluation: fixture exceeds file or byte limit")
		}
		return err
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
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return errors.New("evaluation: workspace is outside run root")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("evaluation: opening run root: %w", err)
	}
	removeErr := rootHandle.RemoveAll(relative)
	closeErr := rootHandle.Close()
	if err := errors.Join(removeErr, closeErr); err != nil {
		return fmt.Errorf("evaluation: clearing incomplete workspace: %w", err)
	}
	return nil
}

func copyFixtureFile(
	ctx context.Context,
	source, destination *os.Root,
	relative string,
	remaining int64,
) (int64, error) {
	output, err := destination.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	copied, mode, copyErr := readRootFile(ctx, source, relative, remaining, output)
	permissions := mode.Perm() & 0o700
	if permissions&0o600 == 0 {
		permissions |= 0o600
	}
	var chmodErr error
	if copyErr == nil {
		chmodErr = output.Chmod(permissions)
	}
	closeErr := output.Close()
	if err := errors.Join(copyErr, chmodErr, closeErr); err != nil {
		_ = destination.Remove(relative)
		return copied, err
	}
	return copied, nil
}
