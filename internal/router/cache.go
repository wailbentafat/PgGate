package router

import (
	"sync"
)

type routeCache struct {
	mu      sync.RWMutex
	entries map[string]Destination
	order   []string
	maxSize int
}

func newRouteCache(maxSize int) *routeCache {
	return &routeCache{
		entries: make(map[string]Destination, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
	}
}

func (c *routeCache) Get(fingerprint string) (Destination, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	dest, ok := c.entries[fingerprint]
	return dest, ok
}

func (c *routeCache) Put(fingerprint string, dest Destination) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.entries[fingerprint]; ok {
		return // already cached
	}

	// Evict oldest if full
	if len(c.order) >= c.maxSize {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}

	c.entries[fingerprint] = dest
	c.order = append(c.order, fingerprint)
}
