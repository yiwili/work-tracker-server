package sync_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/sync"
	"github.com/yiwili/work-tracker-server/internal/testutil"
)

func TestValidationRules(t *testing.T) {
	// Work item validation
	err := sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"date":  "2026-11-06",
		"title": "Fix issue",
	}, true)
	require.NoError(t, err)

	// Missing title on create
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"date": "2026-11-06",
	}, true)
	require.Error(t, err)

	// Whitespace-only title
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"date":  "2026-11-06",
		"title": "   \t  ",
	}, true)
	require.Error(t, err)

	// Invalid calendar date 2026-02-31
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"date":  "2026-02-31",
		"title": "Valid title",
	}, true)
	require.Error(t, err)

	// Fractional confirmed_minutes (e.g. 12.75)
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"date":              "2026-11-06",
		"title":             "Valid title",
		"confirmed_minutes": 12.75,
	}, false)
	require.Error(t, err)

	// Out of range confirmed_minutes
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"confirmed_minutes": 1500,
	}, false)
	require.Error(t, err)

	// Non-boolean time_locked
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"time_locked": "true",
	}, false)
	require.Error(t, err)

	// Non-boolean deleted
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"deleted": 1,
	}, false)
	require.Error(t, err)

	// Unknown field
	err = sync.ValidateFields(sync.TypeWorkItem, map[string]any{
		"date":    "2026-11-06",
		"title":   "Fix",
		"invalid": 123,
	}, false)
	require.Error(t, err)

	// Daily report target_minutes
	err = sync.ValidateFields(sync.TypeDailyReport, map[string]any{
		"target_minutes": 480,
	}, true)
	require.NoError(t, err)

	// GiteeLink validation
	err = sync.ValidateFields(sync.TypeGiteeLink, map[string]any{
		"work_item_id": "wi-123",
		"target_scope": "ent:100",
		"remote_id":    "issue-1",
	}, true)
	require.NoError(t, err)

	// GiteeLink missing remote_id on create
	err = sync.ValidateFields(sync.TypeGiteeLink, map[string]any{
		"work_item_id": "wi-123",
		"target_scope": "ent:100",
	}, true)
	require.Error(t, err)
}

func TestSyncPushAndConflict(t *testing.T) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return
	}

	ctx := context.Background()
	svc := sync.NewService(pool)

	accID := uuid.New()
	devA := uuid.New()
	devB := uuid.New()

	// Create account
	_, err := pool.Exec(ctx, "INSERT INTO accounts (id, email, password_hash) VALUES ($1, $2, 'hash')", accID, "sync_test@example.com")
	require.NoError(t, err)

	// 1. Dev A pushes new work item (base_version 0)
	opID1 := uuid.New()
	pushRes, err := svc.Push(ctx, accID, devA, []sync.SyncOp{
		{
			OpID:        opID1,
			EntityType:  sync.TypeWorkItem,
			EntityID:    "task-1",
			BaseVersion: 0,
			Fields: map[string]any{
				"date":              "2026-11-06",
				"title":             "Original Title",
				"confirmed_minutes": 60,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, pushRes.Results, 1)
	require.Equal(t, "applied", pushRes.Results[0].Status)
	require.Equal(t, int64(1), pushRes.Results[0].Entity.Version)

	// 2. Dev A edits title (base_version 1)
	opID2 := uuid.New()
	pushRes2, err := svc.Push(ctx, accID, devA, []sync.SyncOp{
		{
			OpID:        opID2,
			EntityType:  sync.TypeWorkItem,
			EntityID:    "task-1",
			BaseVersion: 1,
			Fields: map[string]any{
				"title": "Title Modified By A",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "applied", pushRes2.Results[0].Status)
	require.Equal(t, int64(2), pushRes2.Results[0].Entity.Version)

	// 3. Dev B tries to edit title from base_version 1 (conflict!)
	opID3 := uuid.New()
	pushRes3, err := svc.Push(ctx, accID, devB, []sync.SyncOp{
		{
			OpID:        opID3,
			EntityType:  sync.TypeWorkItem,
			EntityID:    "task-1",
			BaseVersion: 1,
			Fields: map[string]any{
				"title": "Conflicting Title By B",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "conflict", pushRes3.Results[0].Status)
	require.Contains(t, pushRes3.Results[0].ConflictFields, "title")
	require.Equal(t, int64(2), pushRes3.Results[0].Entity.Version)

	// 4. Dev B edits confirmed_minutes from base_version 1 (no conflict, orthogonal field!)
	opID4 := uuid.New()
	pushRes4, err := svc.Push(ctx, accID, devB, []sync.SyncOp{
		{
			OpID:        opID4,
			EntityType:  sync.TypeWorkItem,
			EntityID:    "task-1",
			BaseVersion: 1,
			Fields: map[string]any{
				"confirmed_minutes": 90,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "applied", pushRes4.Results[0].Status)
	require.Equal(t, int64(3), pushRes4.Results[0].Entity.Version)

	// 5. Pull changes
	pullRes, err := svc.Pull(ctx, accID, 0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, pullRes.Changes)
	require.Greater(t, pullRes.Cursor, int64(0))

	// 6. Snapshot
	snapRes, err := svc.Snapshot(ctx, accID)
	require.NoError(t, err)
	require.Len(t, snapRes.Entities, 1)
	require.Equal(t, "Title Modified By A", snapRes.Entities[0].Fields["title"])
	require.Equal(t, float64(90), snapRes.Entities[0].Fields["confirmed_minutes"])
}
