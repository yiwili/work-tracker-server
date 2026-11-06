package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yiwili/work-tracker-server/internal/auth"
	"github.com/yiwili/work-tracker-server/internal/config"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

type Module interface {
	Register(rg *gin.RouterGroup)
}

type Deps struct {
	Config      config.Config
	Logger      *slog.Logger
	Pool        *pgxpool.Pool
	AuthHandler *auth.Handler
	Modules     []Module
}

func New(deps Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	if len(deps.Config.TrustedProxies) > 0 {
		_ = r.SetTrustedProxies(deps.Config.TrustedProxies)
	} else {
		_ = r.SetTrustedProxies(nil)
	}

	r.Use(httpx.RequestID())
	r.Use(httpx.Recovery(deps.Logger))
	r.Use(httpx.AccessLog(deps.Logger))
	r.Use(httpx.BodyLimit(deps.Config.MaxBodyBytes))
	r.NoRoute(httpx.NoRoute())

	apiV1 := r.Group("/api/v1")

	// Public health check
	apiV1.GET("/health", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		dbStatus := "ok"
		if deps.Pool != nil {
			if err := deps.Pool.Ping(ctx); err != nil {
				httpx.Fail(c, httpx.E(http.StatusServiceUnavailable, "database_unavailable", "database ping failed"))
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"status":   "ok",
			"database": dbStatus,
			"version":  "1.0.0",
		})
	})

	// Public Auth routes
	if deps.AuthHandler != nil {
		authGroup := apiV1.Group("")
		authenticatedGroup := apiV1.Group("", deps.AuthHandler.Middleware())

		deps.AuthHandler.RegisterRoutes(authGroup, authenticatedGroup)

		// Mount business modules
		for _, m := range deps.Modules {
			m.Register(authenticatedGroup)
		}
	}

	return r
}
