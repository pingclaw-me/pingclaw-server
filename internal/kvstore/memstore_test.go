package kvstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGetSet(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	// Get non-existent key
	_, err := s.Get(ctx, "missing")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}

	// Set and get
	s.Set(ctx, "k1", "v1", 0)
	val, err := s.Get(ctx, "k1")
	if err != nil || val != "v1" {
		t.Fatalf("expected v1, got %q, err=%v", val, err)
	}
}

func TestSetWithTTL(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "k2", "v2", 100*time.Millisecond)

	val, err := s.Get(ctx, "k2")
	if err != nil || val != "v2" {
		t.Fatalf("expected v2 before expiry, got %q, err=%v", val, err)
	}

	time.Sleep(150 * time.Millisecond)

	_, err = s.Get(ctx, "k2")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after expiry, got %v", err)
	}
}

func TestGetDel(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "k3", "v3", 0)

	// GetDel returns and removes
	val, err := s.GetDel(ctx, "k3")
	if err != nil || val != "v3" {
		t.Fatalf("expected v3, got %q, err=%v", val, err)
	}

	// Second GetDel should fail
	_, err = s.GetDel(ctx, "k3")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after GetDel, got %v", err)
	}
}

func TestGetDelExpired(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "k4", "v4", 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	_, err := s.GetDel(ctx, "k4")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for expired key, got %v", err)
	}
}

func TestDel(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "k5", "v5", 0)
	s.Del(ctx, "k5")

	_, err := s.Get(ctx, "k5")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after Del, got %v", err)
	}

	// Del on non-existent key should not error
	err = s.Del(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Del on missing key should not error, got %v", err)
	}
}

func TestIncr(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	// First incr creates the key at 1
	n, err := s.Incr(ctx, "counter")
	if err != nil || n != 1 {
		t.Fatalf("expected 1, got %d, err=%v", n, err)
	}

	n, err = s.Incr(ctx, "counter")
	if err != nil || n != 2 {
		t.Fatalf("expected 2, got %d, err=%v", n, err)
	}

	n, err = s.Incr(ctx, "counter")
	if err != nil || n != 3 {
		t.Fatalf("expected 3, got %d, err=%v", n, err)
	}
}

func TestIncrExpired(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "counter2", "10", 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Incr on expired key should start fresh at 1
	n, err := s.Incr(ctx, "counter2")
	if err != nil || n != 1 {
		t.Fatalf("expected 1 after expiry, got %d, err=%v", n, err)
	}
}

func TestExpire(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "k6", "v6", 0) // no TTL
	s.Expire(ctx, "k6", 100*time.Millisecond)

	val, _ := s.Get(ctx, "k6")
	if val != "v6" {
		t.Fatalf("expected v6 before expiry, got %q", val)
	}

	time.Sleep(150 * time.Millisecond)

	_, err := s.Get(ctx, "k6")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after Expire TTL, got %v", err)
	}
}

func TestTTL(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	// No key
	ttl, _ := s.TTL(ctx, "missing")
	if ttl >= 0 {
		t.Fatalf("expected negative TTL for missing key, got %v", ttl)
	}

	// Key with no TTL
	s.Set(ctx, "k7", "v7", 0)
	ttl, _ = s.TTL(ctx, "k7")
	if ttl >= 0 {
		t.Fatalf("expected negative TTL for no-expiry key, got %v", ttl)
	}

	// Key with TTL
	s.Set(ctx, "k8", "v8", 2*time.Second)
	ttl, err := s.TTL(ctx, "k8")
	if err != nil {
		t.Fatalf("TTL error: %v", err)
	}
	if ttl <= 0 || ttl > 2*time.Second {
		t.Fatalf("expected TTL between 0 and 2s, got %v", ttl)
	}
}

func TestSetOverwrite(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	s.Set(ctx, "k9", "first", 0)
	s.Set(ctx, "k9", "second", 0)

	val, _ := s.Get(ctx, "k9")
	if val != "second" {
		t.Fatalf("expected second, got %q", val)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewMemStore()
	defer s.Close()
	ctx := context.Background()

	done := make(chan struct{}, 200)
	// 100 writers
	for i := 0; i < 100; i++ {
		go func() {
			s.Incr(ctx, "race")
			done <- struct{}{}
		}()
	}
	// 100 readers
	for i := 0; i < 100; i++ {
		go func() {
			s.Get(ctx, "race")
			done <- struct{}{}
		}()
	}

	for i := 0; i < 200; i++ {
		<-done
	}

	val, _ := s.Get(ctx, "race")
	if val != "100" {
		t.Fatalf("expected 100 after concurrent Incr, got %q", val)
	}
}
