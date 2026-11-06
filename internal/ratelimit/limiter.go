package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

// Limiter abstracts rate limiting algorithms.
// A Redis implementation (e.g. distributed sliding window or redis-cell)
// can implement this interface in future extensions without changing the handlers.
type Limiter interface {
	Allow(key string) bool
}

type tokenBucket struct {
	tokens     float64
	lastUpdate time.Time
}

type MemoryLimiter struct {
	mu         sync.Mutex
	rate       float64 // tokens per second
	burst      float64
	buckets    map[string]*tokenBucket
	lastPrune  time.Time
	idleExpiry time.Duration
}

func NewMemory(ratePerSec float64, burst int) *MemoryLimiter {
	return &MemoryLimiter{
		rate:       ratePerSec,
		burst:      float64(burst),
		buckets:    make(map[string]*tokenBucket),
		lastPrune:  time.Now(),
		idleExpiry: 5 * time.Minute,
	}
}

func (m *MemoryLimiter) Allow(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Periodic prune
	if now.Sub(m.lastPrune) > 1*time.Minute {
		for k, b := range m.buckets {
			if now.Sub(b.lastUpdate) > m.idleExpiry {
				delete(m.buckets, k)
			}
		}
		m.lastPrune = now
	}

	b, ok := m.buckets[key]
	if !ok {
		m.buckets[key] = &tokenBucket{
			tokens:     m.burst - 1,
			lastUpdate: now,
		}
		return true
	}

	elapsed := now.Sub(b.lastUpdate).Seconds()
	b.lastUpdate = now
	b.tokens += elapsed * m.rate
	if b.tokens > m.burst {
		b.tokens = m.burst
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	return false
}

func Middleware(limiter Limiter, keyFn func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := keyFn(c)
		if key != "" && !limiter.Allow(key) {
			httpx.Fail(c, httpx.E(http.StatusTooManyRequests, "rate_limited", "too many requests, please slow down"))
			return
		}
		c.Next()
	}
}
