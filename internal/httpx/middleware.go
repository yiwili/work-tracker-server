package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	ContextKeyAccountID = "wts_account_id"
	ContextKeyDeviceID  = "wts_device_id"
	ContextKeyRequestID = "wts_request_id"
)

func SetAuth(c *gin.Context, accountID, deviceID uuid.UUID) {
	c.Set(ContextKeyAccountID, accountID)
	c.Set(ContextKeyDeviceID, deviceID)
}

func AccountID(c *gin.Context) uuid.UUID {
	val, ok := c.Get(ContextKeyAccountID)
	if !ok {
		return uuid.Nil
	}
	id, _ := val.(uuid.UUID)
	return id
}

func DeviceID(c *gin.Context) uuid.UUID {
	val, ok := c.Get(ContextKeyDeviceID)
	if !ok {
		return uuid.Nil
	}
	id, _ := val.(uuid.UUID)
	return id
}

func GetRequestID(c *gin.Context) string {
	if val, ok := c.Get(ContextKeyRequestID); ok {
		if s, ok := val.(string); ok && s != "" {
			return s
		}
	}
	return c.GetHeader("X-Request-ID")
}

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-ID")
		if reqID == "" {
			reqID = uuid.NewString()
		}
		c.Set(ContextKeyRequestID, reqID)
		c.Header("X-Request-ID", reqID)
		c.Next()
	}
}

func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start)

		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		logger.Info("http request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"duration_ms", duration.Milliseconds(),
			"request_id", GetRequestID(c),
		)
	}
}

func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				reqID := GetRequestID(c)
				logger.Error("panic recovered", "panic", r, "request_id", reqID)
				Fail(c, E(http.StatusInternalServerError, "internal", "internal server error"))
			}
		}()
		c.Next()
	}
}

func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func NoRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		Fail(c, E(http.StatusNotFound, "not_found", "endpoint not found"))
	}
}
