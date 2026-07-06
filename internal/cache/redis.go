// Package cache provides a Redis-backed implementation of spatial.Cache,
// used by QuadTree to evict cold leaf nodes out of process memory.
package cache

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yaseer/spatialscale/internal/spatial"
)

// RedisCache implements spatial.Cache over a Redis instance. Points are
// gob-encoded — eviction is an occasional, off-the-hot-path operation, so
// stdlib encoding is simpler than reusing the Arrow codec built for the
// query-serialization benchmark.
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisCache connects to the given Redis address (e.g. "localhost:6380").
// ttl is how long evicted node data lives in Redis before expiring; 0 means
// no expiration.
func NewRedisCache(addr string, ttl time.Duration) (*RedisCache, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect to redis at %s: %w", addr, err)
	}
	return &RedisCache{client: client, ttl: ttl}, nil
}

func (c *RedisCache) Store(key string, points []spatial.Point) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(points); err != nil {
		return fmt.Errorf("encode points: %w", err)
	}
	return c.client.Set(context.Background(), key, buf.Bytes(), c.ttl).Err()
}

func (c *RedisCache) Load(key string) ([]spatial.Point, error) {
	data, err := c.client.Get(context.Background(), key).Bytes()
	if err != nil {
		return nil, fmt.Errorf("get key %q: %w", key, err)
	}
	var points []spatial.Point
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&points); err != nil {
		return nil, fmt.Errorf("decode points: %w", err)
	}
	return points, nil
}

func (c *RedisCache) Delete(key string) error {
	return c.client.Del(context.Background(), key).Err()
}

func (c *RedisCache) Close() error {
	return c.client.Close()
}
