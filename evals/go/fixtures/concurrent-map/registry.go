package registry

type Registry struct {
	values map[string]int
}

func New() *Registry {
	return &Registry{values: make(map[string]int)}
}

func (r *Registry) Set(key string, value int) {
	r.values[key] = value
}

func (r *Registry) Get(key string) (int, bool) {
	value, ok := r.values[key]
	return value, ok
}
