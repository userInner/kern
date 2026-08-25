package extract

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
)

func ZIP(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		name := filepath.Join(destination, entry.Name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(name, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		target, err := os.Create(name)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(target, source)
		closeErr := target.Close()
		source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
