// Package redis provides a Redis client wrapper for the Reticora backend.
// It handles connection management, health checks, and graceful shutdown.
package redis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps a go-redis client with application-level helpers.
type Client struct {
	rdb *redis.Client
}

// Connect parses the given Redis URL and establishes a connection.
// It verifies connectivity with a PING before returning.
func Connect(ctx context.Context, redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: parse URL: %w", err)
	}

	opts.DialTimeout = 5 * time.Second
	opts.ReadTimeout = 3 * time.Second
	opts.WriteTimeout = 3 * time.Second
	opts.PoolSize = 20
	opts.MinIdleConns = 5

	rdb := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis: ping failed: %w", err)
	}

	slog.Info("connected to Redis", "addr", opts.Addr)
	return &Client{rdb: rdb}, nil
}

// Unwrap returns the underlying go-redis client for direct use.
func (c *Client) Unwrap() *redis.Client {
	return c.rdb
}

// Ping checks connectivity.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close gracefully shuts down the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}
