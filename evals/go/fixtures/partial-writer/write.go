package write

import "io"

func WriteAll(writer io.Writer, data []byte) error {
	_, err := writer.Write(data)
	return err
}
