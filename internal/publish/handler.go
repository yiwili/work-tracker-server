package publish

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	rg.POST("/publish/batches", h.handleRegisterBatch)
	rg.GET("/publish/batches/:batch_id", h.handleGetBatch)
	rg.GET("/publish/requests", h.handleListRequests)
	rg.POST("/publish/requests/:request_id/claim", h.handleClaim)
	rg.POST("/publish/requests/:request_id/result", h.handleResult)
	rg.POST("/publish/requests/:request_id/reconcile", h.handleReconcile)
	rg.POST("/publish/requests/:request_id/cancel", h.handleCancel)

	rg.POST("/migration/links", h.handleImportLinks)
	rg.GET("/migration/status", h.handleMigrationStatus)
	rg.POST("/migration/complete", h.handleCompleteMigration)
}

func (h *Handler) handleRegisterBatch(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)

	var req RegisterBatchInput
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	batch, statusCode, err := h.svc.RegisterBatch(c.Request.Context(), accountID, deviceID, req)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(statusCode, batch)
}

func (h *Handler) handleGetBatch(c *gin.Context) {
	accountID := httpx.AccountID(c)
	batchID, err := uuid.Parse(c.Param("batch_id"))
	if err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid batch id"))
		return
	}

	batch, err := h.svc.GetBatch(c.Request.Context(), accountID, batchID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, batch)
}

func (h *Handler) handleListRequests(c *gin.Context) {
	accountID := httpx.AccountID(c)
	state := PublishState(c.Query("state"))
	workItemID := c.Query("work_item_id")
	limit, _ := strconv.Atoi(c.Query("limit"))

	requests, err := h.svc.ListRequests(c.Request.Context(), accountID, state, workItemID, limit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"requests": requests,
	})
}

func (h *Handler) handleClaim(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)
	reqID, err := uuid.Parse(c.Param("request_id"))
	if err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid request id"))
		return
	}

	req, err := h.svc.Claim(c.Request.Context(), accountID, deviceID, reqID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *Handler) handleResult(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)
	reqID, err := uuid.Parse(c.Param("request_id"))
	if err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid request id"))
		return
	}

	var input ResultInput
	if err := httpx.BindJSON(c, &input); err != nil {
		httpx.Fail(c, err)
		return
	}

	req, err := h.svc.ReportResult(c.Request.Context(), accountID, deviceID, reqID, input)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *Handler) handleReconcile(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)
	reqID, err := uuid.Parse(c.Param("request_id"))
	if err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid request id"))
		return
	}

	var input ReconcileInput
	if err := httpx.BindJSON(c, &input); err != nil {
		httpx.Fail(c, err)
		return
	}

	req, err := h.svc.Reconcile(c.Request.Context(), accountID, deviceID, reqID, input)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *Handler) handleCancel(c *gin.Context) {
	accountID := httpx.AccountID(c)
	reqID, err := uuid.Parse(c.Param("request_id"))
	if err != nil {
		httpx.Fail(c, httpx.E(http.StatusBadRequest, "validation_failed", "invalid request id"))
		return
	}

	req, err := h.svc.Cancel(c.Request.Context(), accountID, reqID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *Handler) handleImportLinks(c *gin.Context) {
	accountID := httpx.AccountID(c)
	deviceID := httpx.DeviceID(c)

	var input ImportLinksInput
	if err := httpx.BindJSON(c, &input); err != nil {
		httpx.Fail(c, err)
		return
	}

	res, err := h.svc.ImportLinks(c.Request.Context(), accountID, deviceID, input)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

func (h *Handler) handleMigrationStatus(c *gin.Context) {
	accountID := httpx.AccountID(c)

	res, err := h.svc.MigrationStatus(c.Request.Context(), accountID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

func (h *Handler) handleCompleteMigration(c *gin.Context) {
	accountID := httpx.AccountID(c)

	var req struct {
		Expected MigrationCounts `json:"expected"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	res, err := h.svc.CompleteMigration(c.Request.Context(), accountID, req.Expected)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}
