package auth

import (
	"time"

	"github.com/google/uuid"
)

type Account struct {
	ID                    uuid.UUID `json:"id"`
	Email                 string    `json:"email"`
	DisplayName           string    `json:"display_name"`
	UnifiedPublishEnabled bool      `json:"unified_publish_enabled"`
	CreatedAt             time.Time `json:"created_at"`
}

type DeviceInput struct {
	Name          string `json:"name"`
	Platform      string `json:"platform"` // desktop, mobile, web
	ClientVersion string `json:"client_version"`
}

type Device struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	Platform      string     `json:"platform"`
	ClientVersion string     `json:"client_version"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	LastSyncAt    *time.Time `json:"last_sync_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	Current       bool       `json:"current"`
}

type Session struct {
	Token   string  `json:"token"`
	Account Account `json:"account"`
	Device  Device  `json:"device"`
}
