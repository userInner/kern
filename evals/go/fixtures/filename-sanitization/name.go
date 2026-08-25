package name

import "path/filepath"

func Safe(value string) (string, error) {
	return filepath.Base(value), nil
}
