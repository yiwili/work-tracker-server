package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/auth"
	"github.com/yiwili/work-tracker-server/internal/server"
	"github.com/yiwili/work-tracker-server/internal/testutil"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestApp(t *testing.T) (*gin.Engine, context.Context) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return nil, nil
	}

	cfg := testutil.NewTestConfig()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	authSvc := auth.NewService(pool, cfg)
	authHandler := auth.NewHandler(authSvc)

	r := server.New(server.Deps{
		Config:      cfg,
		Logger:      logger,
		Pool:        pool,
		AuthHandler: authHandler,
		Modules:     []server.Module{},
	})

	return r, context.Background()
}

func TestAuthFlow(t *testing.T) {
	r, _ := setupTestApp(t)
	if r == nil {
		return
	}

	// 1. Register account 1
	regBody := bytes.NewBufferString(`{
		"email": "user1@example.com",
		"password": "password123",
		"display_name": "User One"
	}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/register", regBody)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var regRes auth.Account
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &regRes))
	require.Equal(t, "user1@example.com", regRes.Email)

	// Duplicate register -> 409
	regBodyDup := bytes.NewBufferString(`{
		"email": "user1@example.com",
		"password": "password123",
		"display_name": "User Dup"
	}`)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/auth/register", regBodyDup)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code)

	// 2. Login device 1
	loginBody1 := bytes.NewBufferString(`{
		"email": "user1@example.com",
		"password": "password123",
		"device": {
			"name": "Desktop A",
			"platform": "desktop",
			"client_version": "1.0.0"
		}
	}`)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/auth/login", loginBody1)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var sess1 auth.Session
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sess1))
	require.NotEmpty(t, sess1.Token)

	// Login device 2
	loginBody2 := bytes.NewBufferString(`{
		"email": "user1@example.com",
		"password": "password123",
		"device": {
			"name": "Mobile B",
			"platform": "mobile",
			"client_version": "1.0.0"
		}
	}`)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/auth/login", loginBody2)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var sess2 auth.Session
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sess2))

	// 3. /me on device 1
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+sess1.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// 4. List devices on device 1
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/devices", nil)
	req.Header.Set("Authorization", "Bearer "+sess1.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var devList struct {
		Devices []auth.Device `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &devList))
	require.Len(t, devList.Devices, 2)

	// 5. Revoke device 2 from device 1
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/devices/"+sess2.Device.ID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+sess1.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// 6. Device 2 token is now 401
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+sess2.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 7. Revoke unknown / cross-account device -> 404
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/devices/"+uuid.NewString(), nil)
	req.Header.Set("Authorization", "Bearer "+sess1.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 8. Logout device 1
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+sess1.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// Device 1 token is now 401
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+sess1.Token)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
