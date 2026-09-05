package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

// Auth rate-limit defaults (issue #255).
const (
	authLoginFailLimit     = 10
	authLoginFailWindow    = 15 * time.Minute
	authRegisterLimit      = 5
	authRegisterWindow     = time.Hour
	authRefreshLimit       = 60
	authRefreshWindow      = time.Hour
	authRateLimitKeyPrefix = "oa:ratelimit:auth:"
)

type rateLimitBackend interface {
	count(ctx context.Context, key string) (int, time.Duration, error)
	increment(ctx context.Context, key string, window time.Duration) (int, time.Duration, error)
}

type authRateLimiter struct {
	backend rateLimitBackend
}

var authLimiter *authRateLimiter

func initAuthRateLimiter() {
	var backend rateLimitBackend
	if redisClient != nil {
		backend = &redisRateBackend{}
	} else {
		backend = &memoryRateBackend{}
	}
	authLimiter = &authRateLimiter{backend: backend}
}

func (l *authRateLimiter) isLimited(ctx context.Context, key string, limit int) (bool, time.Duration) {
	if l == nil || l.backend == nil {
		return false, 0
	}
	count, ttl, err := l.backend.count(ctx, key)
	if err != nil {
		return false, 0
	}
	if count >= limit {
		return true, clampRetryAfter(ttl)
	}
	return false, 0
}

func (l *authRateLimiter) recordHit(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration) {
	if l == nil || l.backend == nil {
		return false, 0
	}
	count, ttl, err := l.backend.increment(ctx, key, window)
	if err != nil {
		return false, 0
	}
	if count > limit {
		return true, clampRetryAfter(ttl)
	}
	return false, 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d < time.Second {
		return time.Second
	}
	return d
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, 429, "Too many requests; try again later")
}

func authRateLimitKey(parts ...string) string {
	return authRateLimitKeyPrefix + strings.Join(parts, ":")
}

func normalizeAuthUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// clientIP returns the best-effort client address for rate limiting.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

type memBucket struct {
	mu        sync.Mutex
	count     int
	expiresAt time.Time
}

type memoryRateBackend struct {
	buckets sync.Map
}

func (m *memoryRateBackend) bucket(key string) *memBucket {
	v, _ := m.buckets.LoadOrStore(key, &memBucket{})
	return v.(*memBucket)
}

func (m *memoryRateBackend) count(_ context.Context, key string) (int, time.Duration, error) {
	b := m.bucket(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if !b.expiresAt.IsZero() && now.After(b.expiresAt) {
		return 0, 0, nil
	}
	ttl := time.Until(b.expiresAt)
	if ttl < 0 {
		ttl = 0
	}
	return b.count, ttl, nil
}

func (m *memoryRateBackend) increment(_ context.Context, key string, window time.Duration) (int, time.Duration, error) {
	b := m.bucket(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.expiresAt.IsZero() || now.After(b.expiresAt) {
		b.count = 0
		b.expiresAt = now.Add(window)
	}
	b.count++
	ttl := time.Until(b.expiresAt)
	if ttl < 0 {
		ttl = 0
	}
	return b.count, ttl, nil
}

type redisRateBackend struct{}

func (r *redisRateBackend) count(ctx context.Context, key string) (int, time.Duration, error) {
	n, err := redisClient.Get(ctx, key).Int()
	if err != nil {
		if err == redis.Nil {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	ttl, err := redisClient.TTL(ctx, key).Result()
	if err != nil {
		return n, 0, err
	}
	return n, ttl, nil
}

func (r *redisRateBackend) increment(ctx context.Context, key string, window time.Duration) (int, time.Duration, error) {
	n, err := redisClient.Incr(ctx, key).Result()
	if err != nil {
		return 0, 0, err
	}
	if n == 1 {
		_ = redisClient.Expire(ctx, key, window).Err()
	}
	ttl, err := redisClient.TTL(ctx, key).Result()
	if err != nil {
		return int(n), 0, err
	}
	return int(n), ttl, nil
}
