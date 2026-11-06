package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/yiwili/work-tracker-server/internal/httpx"
	"github.com/yiwili/work-tracker-server/internal/ratelimit"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			httpx.Fail(c, httpx.E(http.StatusUnauthorized, "unauthorized", "missing Authorization header"))
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			httpx.Fail(c, httpx.E(http.StatusUnauthorized, "unauthorized", "invalid Authorization header scheme"))
			return
		}

		rawToken := strings.TrimSpace(parts[1])
		accountID, deviceID, err := h.svc.Authenticate(c.Request.Context(), rawToken)
		if err != nil {
			httpx.Fail(c, err)
			return
		}

		httpx.SetAuth(c, accountID, deviceID)
		c.Next()
	}
}

func (h *Handler) RegisterRoutes(publicRG, authRG *gin.RouterGroup) {
	// Rate limit: 10 per minute per IP, burst 10
	limiter := ratelimit.NewMemory(10.0/60.0, 10)
	rlMiddleware := ratelimit.Middleware(limiter, func(c *gin.Context) string {
		return c.ClientIP()
	})

	publicRG.POST("/auth/register", rlMiddleware, h.handleRegister)
	publicRG.POST("/auth/login", rlMiddleware, h.handleLogin)

	authRG.POST("/auth/logout", h.handleLogout)
	authRG.GET("/me", h.handleMe)
	authRG.GET("/devices", h.handleListDevices)
	authRG.DELETE("/devices/:device_id", h.handleRevokeDevice)
}

func (h *Handler) handleRegister(c *gin.Context) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	acc, err := h.svc.Register(c.Request.Context(), req.Email, req.Password, req.DisplayName)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusCreated, acc)
}

func (h *Handler) handleLogin(c *gin.Context) {
	var req struct {
		Email    string      `json:"email"`
		Password string      `json:"password"`
		Device   DeviceInput `json:"device"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	session, err := h.svc.Login(c.Request.Context(), req.Email, req.Password, req.Device)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, session)
}

func (h *Handler) handleLogout(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	rawToken := strings.TrimSpace(parts[1])

	if err := h.svc.Logout(c.Request.Context(), rawToken); err != nil {
		httpx.Fail(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *Handler) handleMe(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)

	acc, dev, err := h.svc.Me(c.Request.Context(), accountID, deviceID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"account": acc,
		"device":  dev,
	})
}

func (h *Handler) handleListDevices(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)

	devices, err := h.svc.ListDevices(c.Request.Context(), accountID, deviceID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	if devices == nil {
		devices = []Device{}
	}

	c.JSON(http.StatusOK, gin.H{
		"devices": devices,
	})
}

func (h *Handler) handleRevokeDevice(c *gin.Context) {
	accountID := httpx.AccountID(c)
	devIDStr := c.Param("device_id")

	devID, err := uuid.Parse(devIDStr)
	if err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid device id format"))
		return
	}

	if err := h.svc.RevokeDevice(c.Request.Context(), accountID, devID); err != nil {
		httpx.Fail(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}
