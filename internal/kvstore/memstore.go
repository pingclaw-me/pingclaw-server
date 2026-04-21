package kvstore

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// entry is a single key-value pair with an optional expiry.
type entry struct {
	value     string
	expiresAt time.Time // zero means no expiry
}

func (e *entry) expired() bool {
	return !e.expiresAt.IsZero() && time.Now().After(e.expiresAt)
}

// MemStore is an in-memory KVStore backed by sync.Map. It runs a
// background goroutine that evicts expired entries every 30 seconds.
// Used in --local mode as a drop-in replacement for Redis.
type MemStore struct {
	mu   sync.Mutex
	data map[string]*entry
	done chan struct{}
}

// NewMemStore creates a MemStore and starts the cleanup goroutine.
// Call Close() to stop the goroutine when the store is no longer needed.
func NewMemStore() *MemStore {
	s := &MemStore{
		data: make(map[string]*entry),
		done: make(chan struct{}),
	}
	go s.cleanup()
	return s
}

// Close stops the background cleanup goroutine.
func (s *MemStore) Close() {
	close(s.done)
}

func (s *MemStore) cleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			for k, e := range s.data {
				if e.expired() {
					delete(s.data, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *MemStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || e.expired() {
		delete(s.data, key)
		return "", ErrKeyNotFound
	}
	return e.value, nil
}

func (s *MemStore) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := &entry{value: value}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	s.data[key] = e
	return nil
}

func (s *MemStore) GetDel(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || e.expired() {
		delete(s.data, key)
		return "", ErrKeyNotFound
	}
	delete(s.data, key)
	return e.value, nil
}

func (s *MemStore) Del(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *MemStore) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || e.expired() {
		s.data[key] = &entry{value: "1"}
		return 1, nil
	}
	n, _ := strconv.ParseInt(e.value, 10, 64)
	n++
	e.value = strconv.FormatInt(n, 10)
	return n, nil
}

func (s *MemStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || e.expired() {
		return nil
	}
	e.expiresAt = time.Now().Add(ttl)
	return nil
}

func (s *MemStore) TTL(_ context.Context, key string) (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || e.expired() {
		return -1, nil
	}
	if e.expiresAt.IsZero() {
		return -1, nil
	}
	return time.Until(e.expiresAt), nil
}
