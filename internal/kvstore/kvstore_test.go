package kvstore

import (
	"context"
	"errors"
	"testing"
)

// TestErrKeyNotFound verifies the sentinel error works with errors.Is.
func TestErrKeyNotFound(t *testing.T) {
	if !errors.Is(ErrKeyNotFound, ErrKeyNotFound) {
		t.Fatal("ErrKeyNotFound should match itself")
	}

	wrapped := errors.New("wrapped: " + ErrKeyNotFound.Error())
	if errors.Is(wrapped, ErrKeyNotFound) {
		t.Fatal("different error should not match ErrKeyNotFound")
	}
}

// TestKVStoreInterface verifies that MemStore satisfies KVStore at
// compile time. (RedisStore is verified by its own compilation.)
func TestKVStoreInterface(t *testing.T) {
	var _ KVStore = (*MemStore)(nil)
}

// TestGetReturnsErrKeyNotFound verifies the contract: Get on a missing
// key returns ErrKeyNotFound, not nil error.
func TestGetReturnsErrKeyNotFound(t *testing.T) {
	s := NewMemStore()
	defer s.Close()

	_, err := s.Get(context.Background(), "nonexistent")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

// TestGetDelReturnsErrKeyNotFound same contract for GetDel.
func TestGetDelReturnsErrKeyNotFound(t *testing.T) {
	s := NewMemStore()
	defer s.Close()

	_, err := s.GetDel(context.Background(), "nonexistent")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}
