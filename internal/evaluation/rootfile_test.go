package evaluation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRootFileHonorsCancellationDuringRead(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "large.txt"), make([]byte, 128<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(t.Context())

	_, _, err = readRootFile(ctx, root, "large.txt", 128<<10, cancelingWriter{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readRootFile(canceled) error = %v, want context.Canceled", err)
	}
}

func TestReadRootFileRejectsReplacementDuringRead(t *testing.T) {
	rootDir := t.TempDir()
	name := filepath.Join(rootDir, "input.txt")
	if err := os.WriteFile(name, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	writer := &replacingWriter{path: name}
	_, _, err = readRootFile(t.Context(), root, "input.txt", 32, writer)
	if err == nil {
		t.Fatal("readRootFile(replaced) error = nil")
	}
}

type cancelingWriter struct {
	cancel context.CancelFunc
}

func (w cancelingWriter) Write(buffer []byte) (int, error) {
	w.cancel()
	return len(buffer), nil
}

type replacingWriter struct {
	path     string
	replaced bool
}

func (w *replacingWriter) Write(buffer []byte) (int, error) {
	if !w.replaced {
		w.replaced = true
		if err := os.Rename(w.path, w.path+".old"); err != nil {
			return 0, err
		}
		if err := os.WriteFile(w.path, []byte("replacement"), 0o600); err != nil {
			return 0, err
		}
	}
	return io.Discard.Write(buffer)
}
