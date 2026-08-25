package worker

import (
	"context"
	"time"
)

func Process(_ context.Context, items []string) error {
	for range items {
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}
