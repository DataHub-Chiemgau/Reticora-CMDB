package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
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

// incrementBelow increments KEYS[1] when its value is below ARGV[1] and
// renews its TTL (ARGV[2] milliseconds, 0: none) as Increment does; it
// returns {incremented, value of KEYS[1], value of KEYS[2]}, missing
// counters being 0.
var incrementBelow = redis.NewScript(`
local limit, ttl = tonumber(ARGV[1]), tonumber(ARGV[2])
local count = tonumber(redis.call('GET', KEYS[1]) or '0') or 0
local added = 0
if count < limit then
  count = redis.call('INCR', KEYS[1])
  if ttl > 0 then
    redis.call('PEXPIRE', KEYS[1], ttl)
  end
  added = 1
end
local other = tonumber(redis.call('GET', KEYS[2]) or '0') or 0
return {added, count, other}
`)

func (r *RedisStore) IncrementBelow(ctx context.Context, key string, limit int64, ttl time.Duration, other string) (BoundedCount, error) {
	res, err := incrementBelow.Run(ctx, r.client, []string{key, other}, limit, ttl.Milliseconds()).Int64Slice()
	if err != nil {
		return BoundedCount{}, fmt.Errorf("cache: redis increment below: %w", err)
	}
	if len(res) != 3 {
		return BoundedCount{}, fmt.Errorf("cache: redis increment below: unexpected reply %v", res)
	}
	return BoundedCount{Added: res[0] == 1, Count: res[1], Other: res[2]}, nil
}

func (r *RedisStore) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache: redis del: %w", err)
	}
	return nil
}

// slidingWindow keeps the events of KEYS[1] as a sorted set scored by
// their time in microseconds (exact in a double). ARGV: now, window,
// limit (0: count only), member of the new event. It drops the events at
// or before now-window, adds the new one when fewer than limit remain and
// returns {added, count, score of the oldest event or -1}. The key expires
// when its newest event leaves the window.
var slidingWindow = redis.NewScript(`
local now, window, limit = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
local count = redis.call('ZCARD', KEYS[1])
local added = 0
if limit > 0 and count < limit then
  redis.call('ZADD', KEYS[1], now, ARGV[4])
  redis.call('PEXPIRE', KEYS[1], math.ceil(window / 1000))
  count = count + 1
  added = 1
end
local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
if oldest[2] == nil then
  return {added, count, -1}
end
return {added, count, tostring(oldest[2])}
`)

func (r *RedisStore) WindowAdd(ctx context.Context, key string, now time.Time, window time.Duration, limit int64) (Window, error) {
	if limit <= 0 {
		return r.WindowCount(ctx, key, now, window)
	}
	return r.window(ctx, key, now, window, limit)
}

func (r *RedisStore) WindowCount(ctx context.Context, key string, now time.Time, window time.Duration) (Window, error) {
	return r.window(ctx, key, now, window, 0)
}

func (r *RedisStore) window(ctx context.Context, key string, now time.Time, window time.Duration, limit int64) (Window, error) {
	// The member only has to be unique: two events of the same
	// microsecond are two entries.
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return Window{}, fmt.Errorf("cache: window member: %w", err)
	}
	at := now.UnixMicro()
	res, err := slidingWindow.Run(ctx, r.client, []string{key},
		at, window.Microseconds(), limit, strconv.FormatInt(at, 10)+"-"+hex.EncodeToString(nonce)).Slice()
	if err != nil {
		return Window{}, fmt.Errorf("cache: redis window: %w", err)
	}
	if len(res) != 3 {
		return Window{}, fmt.Errorf("cache: redis window: unexpected reply %v", res)
	}
	added, _ := res[0].(int64)
	count, _ := res[1].(int64)
	w := Window{Added: added == 1, Count: count}
	if s, ok := res[2].(string); ok {
		micros, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return Window{}, fmt.Errorf("cache: redis window: oldest score %q: %w", s, perr)
		}
		w.Oldest = time.UnixMicro(int64(micros))
	}
	return w, nil
}

func (r *RedisStore) Close() error {
	// The Redis client lifecycle is managed externally.
	return nil
}
