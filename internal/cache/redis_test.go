package cache

import (
	"testing"
	"time"

	"github.com/yaseer/spatialscale/internal/spatial"
)

// redisAddr is the local dev Redis started via:
//
//	docker run -d --name spatiascale-redis -p 6380:6379 redis:7-alpine
const redisAddr = "localhost:6380"

func newTestCache(t *testing.T) *RedisCache {
	t.Helper()
	c, err := NewRedisCache(redisAddr, time.Minute)
	if err != nil {
		t.Skipf("redis not available at %s: %v", redisAddr, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRedisCacheStoreLoad(t *testing.T) {
	c := newTestCache(t)

	points := []spatial.Point{
		{ID: 1, X: 1.5, Y: 2.5, Payload: []byte("Snap25:3")},
		{ID: 2, X: 3.5, Y: 4.5, Payload: []byte("Gad1:1")},
	}
	key := "test-node-store-load"
	if err := c.Store(key, points); err != nil {
		t.Fatalf("Store: %v", err)
	}
	defer c.Delete(key)

	got, err := c.Load(key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(points) {
		t.Fatalf("got %d points, want %d", len(got), len(points))
	}
	for i := range points {
		if got[i].ID != points[i].ID || got[i].X != points[i].X || got[i].Y != points[i].Y ||
			string(got[i].Payload) != string(points[i].Payload) {
			t.Errorf("point[%d]: got %+v, want %+v", i, got[i], points[i])
		}
	}
}

func TestRedisCacheLoadMissingKey(t *testing.T) {
	c := newTestCache(t)
	if _, err := c.Load("does-not-exist"); err == nil {
		t.Error("expected error loading missing key, got nil")
	}
}
