package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store using a Redis backend.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore creates a new Redis-backed cache store.
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func (r *RedisStore) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cache: redis get: %w", err)
	}
	return val, true, nil
}

func (r *RedisStore) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	err := r.client.Set(ctx, key, value, ttl).Err()
	if err != nil {
		return fmt.Errorf("cache: redis set: %w", err)
	}
	return nil
}

func (r *RedisStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, key)
	if ttl > 0 {
		pipe.Expire(ctx, key, ttl)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("cache: redis incr: %w", err)
	}
	return incr.Val(), nil
}

// decrementExisting decrements KEYS[1] only when it exists, so a release
// after the window's key expired does not create a counter without TTL.
var decrementExisting = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return redis.call('DECR', KEYS[1])
end
return 0
`)

func (r *RedisStore) Decrement(ctx context.Context, key string) (int64, error) {
	n, err := decrementExisting.Run(ctx, r.client, []string{key}).Int64()
	if err != nil {
		return 0, fmt.Errorf("cache: redis decr: %w", err)
	}
	return n, nil
}

func (r *RedisStore) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache: redis del: %w", err)
	}
	return nil
}

func (r *RedisStore) Close() error {
	// The Redis client lifecycle is managed externally.
	return nil
}
