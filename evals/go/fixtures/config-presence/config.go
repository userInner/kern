package config

import (
	"encoding/json"
	"io"
)

type Config struct {
	Workers int `json:"workers"`
}

func Decode(reader io.Reader) (Config, error) {
	var config Config
	err := json.NewDecoder(reader).Decode(&config)
	if config.Workers == 0 {
		config.Workers = 4
	}
	return config, err
}
