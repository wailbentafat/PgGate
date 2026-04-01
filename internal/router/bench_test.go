package router

import (
	"testing"
)

func BenchmarkRoute_Select(b *testing.B) {
	r := NewRouter()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Route("SELECT * FROM users WHERE id = 42", false)
	}
}

func BenchmarkRoute_Insert(b *testing.B) {
	r := NewRouter()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Route("INSERT INTO users (name) VALUES ('alice')", false)
	}
}

func BenchmarkRoute_CachedSelect(b *testing.B) {
	r := NewRouter()
	// Warm the cache
	r.Route("SELECT * FROM users WHERE id = 1", false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Route("SELECT * FROM users WHERE id = 999", false)
	}
}

func BenchmarkRoute_ComplexCTE(b *testing.B) {
	r := NewRouter()
	query := "WITH active AS (SELECT * FROM users WHERE active = true) SELECT a.*, o.total FROM active a JOIN orders o ON a.id = o.user_id"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Route(query, false)
	}
}

func BenchmarkRouteCache_Put(b *testing.B) {
	c := newRouteCache(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Put(string(rune(i%4096)), Replica)
	}
}

func BenchmarkRouteCache_Get(b *testing.B) {
	c := newRouteCache(4096)
	for i := 0; i < 4096; i++ {
		c.Put(string(rune(i)), Replica)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(string(rune(i % 4096)))
	}
}
