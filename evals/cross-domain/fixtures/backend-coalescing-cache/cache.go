package cache

import "context"

type Loader func(context.Context) (string, error)

type Cache struct { values map[string]string }

func New() *Cache { return &Cache{values: make(map[string]string)} }

func (c *Cache) Get(ctx context.Context, key string, load Loader) (string, error) {
	if value, ok := c.values[key]; ok { return value, nil }
	value, err := load(ctx)
	if err == nil { c.values[key] = value }
	return value, err
}
