// Package ratelimit provides a fixed-window rate limiter backed by a
// KVStore (Redis in hosted mode, in-memory in local mode).
package ratelimit

import (
	"context"
	"time"

	"github.com/pingclaw-me/pingclaw-server/internal/kvstore"
)

// Limiter counts events per key over a fixed window. The window is
// uniform across all keys (e.g. 1 hour). A new window starts when the
// previous one expires; this is simpler than a sliding window and
// accurate enough for abuse prevention.
type Limiter struct {
	kv     kvstore.KVStore
	window time.Duration
}

// New constructs a Limiter.
func New(kv kvstore.KVStore, window time.Duration) *Limiter {
	return &Limiter{kv: kv, window: window}
}

// Allow records one event under `key` and returns whether the caller is
// still within `max` events for the current window.
//
// Returns:
//
//	allowed     — true if the call is within the limit
//	retryAfter  — seconds until the current window expires (only meaningful when allowed==false)
//	err         — store error; the caller should fail-open (allow) on err
//
// On the first event in a window, Expire is set to the limiter's window
// length. Subsequent events in the same window just Incr.
func (l *Limiter) Allow(ctx context.Context, key string, max int) (allowed bool, retryAfter int, err error) {
	count, err := l.kv.Incr(ctx, key)
	if err != nil {
		return true, 0, err // fail open
	}
	if count == 1 {
		// First event in this window — set the expiry. Best-effort; if
		// Expire fails the key will live forever, which is wrong but
		// rare enough not to fight store errors here.
		_ = l.kv.Expire(ctx, key, l.window)
	}
	if count <= int64(max) {
		return true, 0, nil
	}
	ttl, ttlErr := l.kv.TTL(ctx, key)
	if ttlErr != nil || ttl < 0 {
		ttl = l.window
	}
	return false, int(ttl.Seconds()), nil
}
