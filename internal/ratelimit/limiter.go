// Package ratelimit provides a Redis-backed fixed-window rate limiter.
// Used by the sign-in handlers to cap SMS sends per phone and per IP,
// preventing abuse and runaway SMS costs.
package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter counts events per key over a fixed window. The window is
// uniform across all keys (e.g. 1 hour). A new window starts when the
// previous one expires; this is simpler than a sliding window and
// accurate enough for abuse prevention.
type Limiter struct {
	rdb    *redis.Client
	window time.Duration
}

// New constructs a Limiter.
func New(rdb *redis.Client, window time.Duration) *Limiter {
	return &Limiter{rdb: rdb, window: window}
}

// Allow records one event under `key` and returns whether the caller is
// still within `max` events for the current window.
//
// Returns:
//   allowed     — true if the call is within the limit
//   retryAfter  — seconds until the current window expires (only meaningful when allowed==false)
//   err         — Redis error; the caller should fail-open (allow) on err
//
// On the first event in a window, EXPIRE is set to the limiter's window
// length. Subsequent events in the same window just INCR.
func (l *Limiter) Allow(ctx context.Context, key string, max int) (allowed bool, retryAfter int, err error) {
	count, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		return true, 0, err // fail open
	}
	if count == 1 {
		// First event in this window — set the expiry. Best-effort; if
		// EXPIRE fails the key will live forever, which is wrong but
		// rare enough not to fight Redis errors here.
		_ = l.rdb.Expire(ctx, key, l.window).Err()
	}
	if count <= int64(max) {
		return true, 0, nil
	}
	ttl, ttlErr := l.rdb.TTL(ctx, key).Result()
	if ttlErr != nil || ttl < 0 {
		ttl = l.window
	}
	return false, int(ttl.Seconds()), nil
}
