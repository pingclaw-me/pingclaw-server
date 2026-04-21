package kvstore

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore wraps a *redis.Client and implements KVStore. Used in
// hosted mode — behaviour is identical to calling Redis directly.
type RedisStore struct {
	rdb *redis.Client
}

// NewRedisStore creates a RedisStore from an existing Redis client.
func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	val, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrKeyNotFound
	}
	return val, err
}

func (s *RedisStore) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, value, ttl).Err()
}

func (s *RedisStore) GetDel(ctx context.Context, key string) (string, error) {
	val, err := s.rdb.GetDel(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrKeyNotFound
	}
	return val, err
}

func (s *RedisStore) Del(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, key).Err()
}

func (s *RedisStore) Incr(ctx context.Context, key string) (int64, error) {
	return s.rdb.Incr(ctx, key).Result()
}

func (s *RedisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.rdb.Expire(ctx, key, ttl).Err()
}

func (s *RedisStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	return s.rdb.TTL(ctx, key).Result()
}
