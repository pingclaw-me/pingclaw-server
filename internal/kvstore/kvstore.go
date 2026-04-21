// Package kvstore defines a key-value store interface used by the
// PingClaw handler for caching, location storage, rate limiting, and
// single-use codes. Two implementations exist:
//
//   - RedisStore wraps a Redis client (used in hosted mode)
//   - MemStore is an in-memory implementation (used in --local mode)
package kvstore

import (
	"context"
	"errors"
	"time"
)

// ErrKeyNotFound is returned by Get and GetDel when the key does not
// exist (or has expired). Callers should use errors.Is to check.
var ErrKeyNotFound = errors.New("key not found")

// KVStore is the interface the handler uses for all ephemeral state
// that would otherwise go through Redis.
type KVStore interface {
	// Get returns the value for key, or ErrKeyNotFound.
	Get(ctx context.Context, key string) (string, error)

	// Set stores a value with the given TTL. A zero TTL means no expiry.
	Set(ctx context.Context, key string, value string, ttl time.Duration) error

	// GetDel atomically returns and deletes the value, or ErrKeyNotFound.
	GetDel(ctx context.Context, key string) (string, error)

	// Del removes a key. No error if the key doesn't exist.
	Del(ctx context.Context, key string) error

	// Incr increments a numeric key by 1, creating it at "1" if absent.
	// Returns the new value.
	Incr(ctx context.Context, key string) (int64, error)

	// Expire sets or updates the TTL on an existing key.
	Expire(ctx context.Context, key string, ttl time.Duration) error

	// TTL returns the remaining time-to-live, or a negative duration if
	// the key has no expiry or does not exist.
	TTL(ctx context.Context, key string) (time.Duration, error)
}
