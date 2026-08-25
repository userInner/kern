package retry

import "context"

func Do(_ context.Context, attempts int, operation func() error) error {
	var last error
	for range attempts {
		if err := operation(); err != nil {
			last = err
			continue
		}
		return nil
	}
	return last
}
