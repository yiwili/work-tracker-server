package sync

import (
	"time"

	"github.com/google/uuid"
)

type EntityType string

const (
	TypeWorkItem       EntityType = "work_item"
	TypeDailyReport    EntityType = "daily_report"
	TypeSetting        EntityType = "setting"
	TypeProjectMapping EntityType = "project_mapping"
	TypeGiteeLink      EntityType = "gitee_link"
)

func IsValidEntityType(t string) bool {
	switch EntityType(t) {
	case TypeWorkItem, TypeDailyReport, TypeSetting, TypeProjectMapping, TypeGiteeLink:
		return true
	default:
		return false
	}
}

func IsClientWritable(t EntityType) bool {
	return t != TypeGiteeLink
}

type Entity struct {
	EntityType    EntityType       `json:"entity_type"`
	EntityID      string           `json:"entity_id"`
	Version       int64            `json:"version"`
	FieldVersions map[string]int64 `json:"field_versions"`
	Fields        map[string]any   `json:"fields"`
	UpdatedAt     time.Time        `json:"updated_at"`
	UpdatedBy     *uuid.UUID       `json:"updated_by_device"`
}

type SyncOp struct {
	OpID        uuid.UUID      `json:"op_id"`
	EntityType  EntityType     `json:"entity_type"`
	EntityID    string         `json:"entity_id"`
	BaseVersion int64          `json:"base_version"`
	Fields      map[string]any `json:"fields"`
}

type SyncOpResult struct {
	OpID           uuid.UUID `json:"op_id"`
	Status         string    `json:"status"` // applied, conflict, rejected
	Entity         *Entity   `json:"entity,omitempty"`
	ConflictFields []string  `json:"conflict_fields,omitempty"`
	Error          *OpError  `json:"error,omitempty"`
}

type OpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PushRequest struct {
	Ops []SyncOp `json:"ops"`
}

type PushResponse struct {
	Results []SyncOpResult `json:"results"`
	Cursor  int64          `json:"cursor"`
}

type PullResponse struct {
	Changes []Entity `json:"changes"`
	Cursor  int64    `json:"cursor"`
	HasMore bool     `json:"has_more"`
}

type SnapshotResponse struct {
	Entities []Entity `json:"entities"`
	Cursor   int64    `json:"cursor"`
}
