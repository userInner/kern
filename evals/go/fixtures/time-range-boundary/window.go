package window

import (
	"errors"
	"time"
)

type Window struct {
	start time.Time
	end   time.Time
}

func New(start, end time.Time) (Window, error) {
	if end.Before(start) {
		return Window{}, errors.New("end precedes start")
	}
	return Window{start: start, end: end}, nil
}

func (w Window) Contains(value time.Time) bool {
	return !value.Before(w.start) && !value.After(w.end)
}
