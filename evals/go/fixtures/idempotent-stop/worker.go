package worker

type Worker struct {
	done chan struct{}
}

func New() *Worker {
	return &Worker{done: make(chan struct{})}
}

func (w *Worker) Done() <-chan struct{} {
	return w.done
}

func (w *Worker) Stop() {
	close(w.done)
}
