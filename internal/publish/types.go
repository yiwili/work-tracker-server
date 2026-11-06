package publish

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yiwili/work-tracker-server/internal/sync"
)

type EntityStore interface {
	LockAccount(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error
	GetEntity(ctx context.Context, q sync.QueryRower, accountID uuid.UUID, entityType, entityID string) (*sync.Entity, error)
	PutSystemEntity(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, deviceID *uuid.UUID, entityType, entityID string, fields map[string]any) (*sync.Entity, error)
}

type PublishState string

const (
	StateRegistered PublishState = "registered"
	StateSending    PublishState = "sending"
	StateSucceeded  PublishState = "succeeded"
	StateFailed     PublishState = "failed"
	StateUncertain  PublishState = "uncertain"
	StateExpired    PublishState = "expired"
	StateCancelled  PublishState = "cancelled"
)

type PublishRequest struct {
	ID                uuid.UUID      `json:"id"`
	BatchID           *uuid.UUID     `json:"batch_id"`
	WorkItemID        string         `json:"work_item_id"`
	WorkItemVersion   int64          `json:"work_item_version"`
	TargetScope       string         `json:"target_scope"`
	Operation         string         `json:"operation"` // create, update
	OperationAdjusted bool           `json:"operation_adjusted"`
	RemoteID          string         `json:"remote_id"`
	URL               string         `json:"url"`
	Target            map[string]any `json:"target"`
	Payload           map[string]any `json:"payload"`
	ConfigVersions    map[string]int `json:"config_versions"`
	Executor          string         `json:"executor"` // device, cloud
	ExecutorDeviceID  *uuid.UUID     `json:"executor_device_id"`
	State             PublishState   `json:"state"`
	Error             string         `json:"error"`
	Note              string         `json:"note"`
	PublishedVersion  *int64         `json:"published_version"`
	ExpiresAt         *time.Time     `json:"expires_at"`
	ClaimedAt         *time.Time     `json:"claimed_at"`
	FinishedAt        *time.Time     `json:"finished_at"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type PublishBatch struct {
	ID              uuid.UUID        `json:"id"`
	IdempotencyKey  string           `json:"idempotency_key"`
	Executor        string           `json:"executor"`
	CreatedByDevice *uuid.UUID       `json:"created_by_device"`
	CreatedAt       time.Time        `json:"created_at"`
	Requests        []PublishRequest `json:"requests"`
}

type PublishItemInput struct {
	WorkItemID      string         `json:"work_item_id"`
	WorkItemVersion int64          `json:"work_item_version"`
	TargetScope     string         `json:"target_scope"`
	Operation       string         `json:"operation"`
	RemoteID        string         `json:"remote_id"`
	Target          map[string]any `json:"target"`
	Payload         map[string]any `json:"payload"`
	ConfigVersions  map[string]int `json:"config_versions"`
}

type RegisterBatchInput struct {
	IdempotencyKey string             `json:"idempotency_key"`
	Executor       string             `json:"executor"`
	Items          []PublishItemInput `json:"items"`
}

type ResultInput struct {
	Outcome  string `json:"outcome"` // succeeded, failed, uncertain
	RemoteID string `json:"remote_id"`
	URL      string `json:"url"`
	Error    string `json:"error"`
}

type ReconcileInput struct {
	Found            bool   `json:"found"`
	RemoteID         string `json:"remote_id"`
	URL              string `json:"url"`
	ProvenNotCreated bool   `json:"proven_not_created,omitempty"`
}

type MigrationCounts struct {
	WorkItems int `json:"work_items"`
	Links     int `json:"links"`
	Uncertain int `json:"uncertain"`
}

type MigrationStatus struct {
	UnifiedPublishEnabled bool            `json:"unified_publish_enabled"`
	Counts                MigrationCounts `json:"counts"`
}

type LinkImportItem struct {
	WorkItemID  string     `json:"work_item_id"`
	TargetScope string     `json:"target_scope"`
	RemoteID    string     `json:"remote_id"`
	URL         string     `json:"url"`
	PublishedAt *time.Time `json:"published_at"`
}

type PendingImportItem struct {
	WorkItemID  string `json:"work_item_id"`
	TargetScope string `json:"target_scope"`
	Note        string `json:"note"`
}

type ImportLinksInput struct {
	Links   []LinkImportItem    `json:"links"`
	Pending []PendingImportItem `json:"pending"`
}
