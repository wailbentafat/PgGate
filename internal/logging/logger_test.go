package logging

import (
	"context"
	"testing"
)

func TestSetup(t *testing.T) {
	// Should not panic with any valid config
	Setup("debug", "json")
	Setup("info", "text")
	Setup("warn", "json")
	Setup("error", "text")

	// Unknown level defaults to info
	Setup("unknown", "json")
}

func TestRequestID(t *testing.T) {
	ctx := context.Background()

	// No request ID
	if rid := RequestID(ctx); rid != "" {
		t.Errorf("expected empty request ID, got %q", rid)
	}

	// With request ID
	ctx = WithRequestID(ctx, "test-123")
	if rid := RequestID(ctx); rid != "test-123" {
		t.Errorf("expected 'test-123', got %q", rid)
	}
}

func TestFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = WithRequestID(ctx, "req-456")

	l := FromContext(ctx)
	if l == nil {
		t.Fatal("logger should not be nil")
	}
}

func TestL(t *testing.T) {
	l := L()
	if l == nil {
		t.Fatal("global logger should not be nil")
	}
}
