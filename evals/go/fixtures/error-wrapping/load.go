package load

import (
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("not found")

func Record(id string) error {
	return fmt.Errorf("load record %q: %v", id, ErrNotFound)
}
