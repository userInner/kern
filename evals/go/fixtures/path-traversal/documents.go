package documents

import (
	"os"
	"path/filepath"
)

func ReadDocument(root, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, name))
}
