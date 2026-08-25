package decode

import (
	"encoding/json"
	"io"
)

const MaxBodyBytes = 64

type Request struct {
	Message string `json:"message"`
}

func DecodeRequest(reader io.Reader) (Request, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Request{}, err
	}
	var request Request
	err = json.Unmarshal(data, &request)
	return request, err
}
