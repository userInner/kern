package cache

type Cache struct {
	values map[string][]byte
}

func New() *Cache {
	return &Cache{values: make(map[string][]byte)}
}

func (c *Cache) Set(key string, value []byte) {
	c.values[key] = value
}

func (c *Cache) Get(key string) ([]byte, bool) {
	value, ok := c.values[key]
	return value, ok
}
