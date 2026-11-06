package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

func E(status int, code, msg string) *Error {
	return &Error{
		Status:  status,
		Code:    code,
		Message: msg,
	}
}

func Fail(c *gin.Context, err error) {
	if c.IsAborted() {
		return
	}

	var httpErr *Error
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.Status, gin.H{
			"error": httpErr,
		})
		c.Abort()
		return
	}

	// Unknown error: never leak internal details
	reqID := GetRequestID(c)
	slog.Error("internal server error", "error", err, "request_id", reqID)

	c.JSON(http.StatusInternalServerError, gin.H{
		"error": gin.H{
			"code":    "internal",
			"message": "internal server error",
		},
	})
	c.Abort()
}

func BindJSON(c *gin.Context, dst any) error {
	var maxBytesErr *http.MaxBytesError
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		if errors.As(err, &maxBytesErr) {
			return &Error{
				Status:  http.StatusRequestEntityTooLarge,
				Code:    "payload_too_large",
				Message: "request body too large",
			}
		}
		return &Error{
			Status:  http.StatusBadRequest,
			Code:    "validation_failed",
			Message: fmt.Sprintf("invalid JSON payload: %v", err),
		}
	}

	// Ensure no trailing tokens
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return &Error{
			Status:  http.StatusBadRequest,
			Code:    "validation_failed",
			Message: "unexpected content after JSON payload",
		}
	}

	return nil
}
