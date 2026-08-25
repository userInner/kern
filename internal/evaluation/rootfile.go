package evaluation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

var errFileByteLimit = errors.New("evaluation: file byte limit exceeded")

// readRootFile copies at most remaining+1 bytes from a regular file beneath a
// stable root. The extra byte distinguishes an exactly-full file from one that
// grew beyond the caller's remaining budget after directory enumeration.
func readRootFile(
	ctx context.Context,
	root *os.Root,
	relative string,
	remaining int64,
	destination io.Writer,
) (int64, fs.FileMode, error) {
	if root == nil || destination == nil {
		return 0, 0, errors.New("evaluation: file root and destination are required")
	}
	if remaining < 0 {
		return 0, 0, errFileByteLimit
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}

	before, err := root.Lstat(relative)
	if err != nil {
		return 0, 0, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("evaluation: non-regular file %q is not allowed", relative)
	}
	file, err := root.Open(relative)
	if err != nil {
		return 0, 0, err
	}

	opened, statErr := file.Stat()
	current, lstatErr := root.Lstat(relative)
	if err := errors.Join(statErr, lstatErr); err != nil {
		return 0, 0, errors.Join(err, file.Close())
	}
	if current.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() ||
		!current.Mode().IsRegular() || !os.SameFile(before, opened) ||
		!os.SameFile(opened, current) {
		return 0, 0, errors.Join(
			fmt.Errorf("evaluation: file changed while opening: %s", relative),
			file.Close(),
		)
	}

	copied, copyErr := io.Copy(destination, &evaluationContextReader{
		ctx:    ctx,
		reader: io.LimitReader(file, remaining+1),
	})
	if copyErr == nil && copied > remaining {
		copyErr = errFileByteLimit
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	after, afterStatErr := file.Stat()
	final, finalLstatErr := root.Lstat(relative)
	identityErr := errors.Join(afterStatErr, finalLstatErr)
	if identityErr == nil && (final.Mode()&os.ModeSymlink != 0 ||
		!after.Mode().IsRegular() || !final.Mode().IsRegular() ||
		!os.SameFile(opened, after) || !os.SameFile(after, final)) {
		identityErr = fmt.Errorf("evaluation: file changed while reading: %s", relative)
	}
	return copied, opened.Mode(), errors.Join(copyErr, identityErr, file.Close())
}

type evaluationContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *evaluationContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
