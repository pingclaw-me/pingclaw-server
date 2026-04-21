package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/pingclaw-me/pingclaw-server/internal/kvstore"
)

func TestAllowUnderLimit(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, time.Hour)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		ok, _, err := lim.Allow(ctx, "key1", 5)
		if err != nil {
			t.Fatalf("Allow returned error: %v", err)
		}
		if !ok {
			t.Fatalf("request %d should be allowed (limit 5)", i+1)
		}
	}
}

func TestAllowAtLimit(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, time.Hour)
	ctx := context.Background()

	// Use up the limit
	for i := 0; i < 3; i++ {
		ok, _, _ := lim.Allow(ctx, "key2", 3)
		if !ok {
			t.Fatalf("request %d should be allowed (limit 3)", i+1)
		}
	}

	// Next request should be denied
	ok, retryAfter, err := lim.Allow(ctx, "key2", 3)
	if err != nil {
		t.Fatalf("Allow returned error: %v", err)
	}
	if ok {
		t.Fatal("request 4 should be denied (limit 3)")
	}
	if retryAfter <= 0 {
		t.Fatalf("retryAfter should be positive, got %d", retryAfter)
	}
}

func TestAllowDifferentKeys(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, time.Hour)
	ctx := context.Background()

	// Exhaust key A
	for i := 0; i < 2; i++ {
		lim.Allow(ctx, "keyA", 2)
	}
	ok, _, _ := lim.Allow(ctx, "keyA", 2)
	if ok {
		t.Fatal("keyA should be exhausted")
	}

	// Key B should still be allowed
	ok, _, _ = lim.Allow(ctx, "keyB", 2)
	if !ok {
		t.Fatal("keyB should be allowed (independent from keyA)")
	}
}

func TestAllowWindowExpiry(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	// Use a very short window
	lim := New(kv, 100*time.Millisecond)
	ctx := context.Background()

	// Exhaust the limit
	lim.Allow(ctx, "key3", 1)
	ok, _, _ := lim.Allow(ctx, "key3", 1)
	if ok {
		t.Fatal("should be denied after limit")
	}

	// Wait for the window to expire
	time.Sleep(150 * time.Millisecond)

	// Should be allowed again
	ok, _, err := lim.Allow(ctx, "key3", 1)
	if err != nil {
		t.Fatalf("Allow returned error: %v", err)
	}
	if !ok {
		t.Fatal("should be allowed after window expiry")
	}
}

func TestAllowRetryAfterDecreases(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, 5*time.Second)
	ctx := context.Background()

	// Exhaust
	lim.Allow(ctx, "key4", 1)

	_, retryAfter1, _ := lim.Allow(ctx, "key4", 1)

	// Wait long enough for the integer seconds to decrease
	time.Sleep(1500 * time.Millisecond)

	_, retryAfter2, _ := lim.Allow(ctx, "key4", 1)

	if retryAfter2 >= retryAfter1 {
		t.Fatalf("retryAfter should decrease over time: first=%d, second=%d", retryAfter1, retryAfter2)
	}
}

func TestAllowLimitOfOne(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, time.Hour)
	ctx := context.Background()

	ok, _, _ := lim.Allow(ctx, "key5", 1)
	if !ok {
		t.Fatal("first request should be allowed")
	}

	ok, _, _ = lim.Allow(ctx, "key5", 1)
	if ok {
		t.Fatal("second request should be denied (limit 1)")
	}
}

func TestAllowHighLimit(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, time.Hour)
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		ok, _, err := lim.Allow(ctx, "key6", 1000)
		if err != nil {
			t.Fatalf("Allow returned error at %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("request %d should be allowed (limit 1000)", i+1)
		}
	}

	ok, _, _ := lim.Allow(ctx, "key6", 1000)
	if ok {
		t.Fatal("request 1001 should be denied")
	}
}

func TestAllowConcurrent(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	lim := New(kv, time.Hour)
	ctx := context.Background()

	// Fire 100 concurrent requests with a limit of 50
	results := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			ok, _, _ := lim.Allow(ctx, "key7", 50)
			results <- ok
		}()
	}

	allowed := 0
	denied := 0
	for i := 0; i < 100; i++ {
		if <-results {
			allowed++
		} else {
			denied++
		}
	}

	if allowed != 50 {
		t.Fatalf("expected exactly 50 allowed, got %d (denied %d)", allowed, denied)
	}
}
