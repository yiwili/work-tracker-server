package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/ratelimit"
)

func TestMemoryLimiter(t *testing.T) {
	// 1 token per sec, burst of 2
	l := ratelimit.NewMemory(1.0, 2)

	require.True(t, l.Allow("user1"))
	require.True(t, l.Allow("user1"))
	require.False(t, l.Allow("user1")) // exhausted

	// Another key still has quota
	require.True(t, l.Allow("user2"))

	// Sleep 1.1s -> should replenish 1 token
	time.Sleep(1100 * time.Millisecond)
	require.True(t, l.Allow("user1"))
	require.False(t, l.Allow("user1"))
}

func TestLimiterMiddleware(t *testing.T) {
	r := gin.New()
	l := ratelimit.NewMemory(10.0, 1)

	r.Use(ratelimit.Middleware(l, func(c *gin.Context) string {
		return c.ClientIP()
	}))

	r.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/ping", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("GET", "/ping", nil)
	req2.RemoteAddr = "192.0.2.1:1234"
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusTooManyRequests, w2.Code)
}
