package sync

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/sync/push", h.handlePush)
	rg.GET("/sync/pull", h.handlePull)
	rg.GET("/sync/snapshot", h.handleSnapshot)
	rg.GET("/entities/:entity_type", h.handleListEntities)
	rg.GET("/entities/:entity_type/:entity_id", h.handleGetEntity)
}

func (h *Handler) handlePush(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)

	var req PushRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	res, err := h.svc.Push(c.Request.Context(), accountID, deviceID, req.Ops)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

func (h *Handler) handlePull(c *gin.Context) {
	accountID := httpx.AccountID(c)

	cursorStr := c.Query("cursor")
	if cursorStr == "" {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "missing cursor parameter"))
		return
	}

	cursor, err := strconv.ParseInt(cursorStr, 10, 64)
	if err != nil || cursor < 0 {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid cursor"))
		return
	}

	limit := 500
	if limitStr := c.Query("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	res, err := h.svc.Pull(c.Request.Context(), accountID, cursor, limit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

func (h *Handler) handleSnapshot(c *gin.Context) {
	accountID := httpx.AccountID(c)

	res, err := h.svc.Snapshot(c.Request.Context(), accountID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

func (h *Handler) handleListEntities(c *gin.Context) {
	accountID := httpx.AccountID(c)
	entityType := c.Param("entity_type")

	if !IsValidEntityType(entityType) {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid entity_type"))
		return
	}

	from := c.Query("from")
	to := c.Query("to")
	includeDeleted, _ := strconv.ParseBool(c.Query("include_deleted"))

	entities, cursor, err := h.svc.ListEntities(c.Request.Context(), accountID, entityType, from, to, includeDeleted)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"entities": entities,
		"cursor":   cursor,
	})
}

func (h *Handler) handleGetEntity(c *gin.Context) {
	accountID := httpx.AccountID(c)
	entityType := c.Param("entity_type")
	entityID := c.Param("entity_id")

	if !IsValidEntityType(entityType) {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid entity_type"))
		return
	}

	ent, err := h.svc.GetEntity(c.Request.Context(), h.svc.pool, accountID, entityType, entityID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	if ent == nil {
		httpx.Fail(c, httpx.E(http.StatusNotFound, "not_found", "entity not found"))
		return
	}

	c.JSON(http.StatusOK, ent)
}
