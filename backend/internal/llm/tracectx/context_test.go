package tracectx

import (
	"context"
	"errors"
	"testing"
)

func TestWithAndEmitProviderFallbackHook(t *testing.T) {
	called := false
	ctx := WithProviderFallbackHook(context.Background(), func(err error) {
		called = true
	})

	if ok := EmitProviderFallback(ctx, errors.New("x")); !ok {
		t.Fatal("expected fallback hook to be emitted")
	}
	if !called {
		t.Fatal("expected hook to be called")
	}
}

func TestEmitProviderFallbackWithoutHook(t *testing.T) {
	if ok := EmitProviderFallback(context.Background(), errors.New("x")); ok {
		t.Fatal("expected false when no hook exists")
	}
	if ok := EmitProviderFallback(context.TODO(), errors.New("x")); ok {
		t.Fatal("expected false for nil context")
	}
}
