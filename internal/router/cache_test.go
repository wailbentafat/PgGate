package router

import (
	"testing"
)

func TestRouteCache_BasicOps(t *testing.T) {
	c := newRouteCache(3)

	// Miss
	if _, ok := c.Get("fp1"); ok {
		t.Error("expected cache miss")
	}

	// Put and hit
	c.Put("fp1", Replica)
	if dest, ok := c.Get("fp1"); !ok || dest != Replica {
		t.Errorf("expected Replica, got %v (ok=%v)", dest, ok)
	}

	// Duplicate put should not duplicate
	c.Put("fp1", Replica)
	if len(c.order) != 1 {
		t.Errorf("duplicate put should not grow order, got %d", len(c.order))
	}
}

func TestRouteCache_Eviction(t *testing.T) {
	c := newRouteCache(3)

	c.Put("fp1", Replica)
	c.Put("fp2", Primary)
	c.Put("fp3", Replica)

	// Cache is full. Adding fp4 should evict fp1.
	c.Put("fp4", Primary)

	if _, ok := c.Get("fp1"); ok {
		t.Error("fp1 should have been evicted")
	}
	if _, ok := c.Get("fp4"); !ok {
		t.Error("fp4 should be in cache")
	}
	if _, ok := c.Get("fp2"); !ok {
		t.Error("fp2 should still be in cache")
	}
}

func TestRouteCache_Concurrent(t *testing.T) {
	c := newRouteCache(1000)

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				key := string(rune(id*100 + j))
				c.Put(key, Replica)
				c.Get(key)
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
