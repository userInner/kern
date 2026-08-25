package write

import (
	"bytes"
	"testing"
)

type shortWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (writer *shortWriter) Write(data []byte) (int, error) {
	if len(data) > writer.limit {
		data = data[:writer.limit]
	}
	return writer.buffer.Write(data)
}

func TestWriteAllHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{limit: 2}
	if err := WriteAll(writer, []byte("abcdef")); err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}
	if got := writer.buffer.String(); got != "abcdef" {
		t.Fatalf("WriteAll() wrote %q, want abcdef", got)
	}
}
