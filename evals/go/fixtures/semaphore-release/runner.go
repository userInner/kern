package runner

import "errors"

var ErrFailed = errors.New("work failed")

type Runner struct {
	semaphore chan struct{}
}

func New(limit int) *Runner {
	return &Runner{semaphore: make(chan struct{}, limit)}
}

func (r *Runner) Run(fail bool) error {
	r.semaphore <- struct{}{}
	if fail {
		return ErrFailed
	}
	<-r.semaphore
	return nil
}
