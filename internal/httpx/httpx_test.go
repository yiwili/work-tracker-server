package httpx_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestBindJSONDisallowsUnknownFields(t *testing.T) {
	r := gin.New()
	r.POST("/test", func(c *gin.Context) {
		var req struct {
			Name string `json:"name"`
		}
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Valid
	body := bytes.NewBufferString(`{"name":"alice"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/test", body)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Unknown field
	body = bytes.NewBufferString(`{"name":"alice","extra":123}`)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/test", body)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "validation_failed")
}

func TestBodyLimit413(t *testing.T) {
	r := gin.New()
	r.Use(httpx.BodyLimit(10))
	r.POST("/test", func(c *gin.Context) {
		var req struct {
			Data string `json:"data"`
		}
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := bytes.NewBufferString(`{"data":"123456789012345"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/test", body)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Contains(t, w.Body.String(), "payload_too_large")
}

func TestAuthContext(t *testing.T) {
	r := gin.New()
	accID := uuid.New()
	devID := uuid.New()

	r.Use(func(c *gin.Context) {
		httpx.SetAuth(c, accID, devID)
		c.Next()
	})

	r.GET("/me", func(c *gin.Context) {
		require.Equal(t, accID, httpx.AccountID(c))
		require.Equal(t, devID, httpx.DeviceID(c))
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/me", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestRecoveryInternalError(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r := gin.New()
	r.Use(httpx.RequestID())
	r.Use(httpx.Recovery(logger))
	r.GET("/panic", func(c *gin.Context) {
		panic("unexpected explosion")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/panic", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "internal")
	require.NotContains(t, w.Body.String(), "unexpected explosion")
}
