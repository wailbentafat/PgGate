package ratelimit

import (
	"testing"
)

func TestIPRateLimiter_Allow(t *testing.T) {
	rl := NewIPRateLimiter(10, 5) // 10 rps, burst of 5

	// First 5 requests should be allowed (burst)
	for i := 0; i < 5; i++ {
		if !rl.Allow("192.168.1.1") {
			t.Errorf("request %d should be allowed within burst", i)
		}
	}

	// 6th request should be rejected (burst exceeded, no time to refill)
	if rl.Allow("192.168.1.1") {
		t.Error("request beyond burst should be rejected")
	}

	// Different IP should still be allowed
	if !rl.Allow("192.168.1.2") {
		t.Error("different IP should have its own limiter")
	}
}

func TestIPRateLimiter_PerIP(t *testing.T) {
	rl := NewIPRateLimiter(1, 1)

	// Exhaust IP 1
	rl.Allow("10.0.0.1")
	if rl.Allow("10.0.0.1") {
		t.Error("IP 1 should be rate limited")
	}

	// IP 2 should still work
	if !rl.Allow("10.0.0.2") {
		t.Error("IP 2 should not be rate limited")
	}
}
