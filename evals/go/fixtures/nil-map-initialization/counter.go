package counter

type Counter struct {
	values map[string]int
}

func New() *Counter {
	return &Counter{}
}

func (c *Counter) Add(key string) {
	c.values[key]++
}

func (c *Counter) Value(key string) int {
	return c.values[key]
}
