package history

import (
	"net/http"

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
	rg.POST("/history/import", h.handleImport)
	rg.GET("/history", h.handleList)
	rg.GET("/history/summary", h.handleSummary)
}

func (h *Handler) handleImport(c *gin.Context) {
	accountID := httpx.AccountID(c)

	var input ImportHistoryInput
	if err := httpx.BindJSON(c, &input); err != nil {
		httpx.Fail(c, err)
		return
	}

	result, err := h.svc.ImportBatch(c.Request.Context(), accountID, input)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, result)
}

func (h *Handler) handleList(c *gin.Context) {
	accountID := httpx.AccountID(c)

	var filter ListFilter
	if err := c.ShouldBindQuery(&filter); err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", err.Error()))
		return
	}

	result, err := h.svc.List(c.Request.Context(), accountID, filter)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, result)
}

func (h *Handler) handleSummary(c *gin.Context) {
	accountID := httpx.AccountID(c)

	result, err := h.svc.Summary(c.Request.Context(), accountID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, result)
}
