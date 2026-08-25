package builtin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/userInner/kern/internal/tool"
)

func decodeStrict(input json.RawMessage, target any) error {
	if len(input) == 0 || !json.Valid(input) {
		return fmt.Errorf("%w: input must be valid json", tool.ErrInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", tool.ErrInvalidInput, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing json value", tool.ErrInvalidInput)
	}
	return nil
}
