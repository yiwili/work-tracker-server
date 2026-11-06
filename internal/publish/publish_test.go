package publish_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/publish"
	"github.com/yiwili/work-tracker-server/internal/sync"
	"github.com/yiwili/work-tracker-server/internal/testutil"
)

func TestPublishLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return
	}

	ctx := context.Background()
	cfg := testutil.NewTestConfig()
	syncSvc := sync.NewService(pool)
	pubSvc := publish.NewService(pool, cfg, syncSvc)

	accID := uuid.New()
	devID := uuid.New()

	// Create account with unified_publish_enabled = true
	_, err := pool.Exec(ctx, "INSERT INTO accounts (id, email, password_hash, unified_publish_enabled) VALUES ($1, $2, 'hash', true)", accID, "pub_test@example.com")
	require.NoError(t, err)

	// Create a work item
	pushRes, err := syncSvc.Push(ctx, accID, devID, []sync.SyncOp{
		{
			OpID:        uuid.New(),
			EntityType:  sync.TypeWorkItem,
			EntityID:    "task-pub-1",
			BaseVersion: 0,
			Fields: map[string]any{
				"date":              "2026-11-06",
				"title":             "Feature ready to publish",
				"confirmed_minutes": 120,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "applied", pushRes.Results[0].Status)
	wiVer := pushRes.Results[0].Entity.Version

	// 1. Register publication batch
	idempKey := "batch-idemp-" + uuid.NewString()
	batch, statusCode, err := pubSvc.RegisterBatch(ctx, accID, devID, publish.RegisterBatchInput{
		IdempotencyKey: idempKey,
		Executor:       "device",
		Items: []publish.PublishItemInput{
			{
				WorkItemID:      "task-pub-1",
				WorkItemVersion: wiVer,
				TargetScope:     "ent:1001",
				Operation:       "create",
				Target:          map[string]any{"program_id": 11},
				Payload:         map[string]any{"title": "Feature ready to publish", "minutes": 120},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 201, statusCode)
	require.Len(t, batch.Requests, 1)
	reqID := batch.Requests[0].ID
	require.Equal(t, publish.StateRegistered, batch.Requests[0].State)

	// 2. Claim request
	claimed, err := pubSvc.Claim(ctx, accID, devID, reqID)
	require.NoError(t, err)
	require.Equal(t, publish.StateSending, claimed.State)
	require.Equal(t, &devID, claimed.ExecutorDeviceID)

	// 3. Report success
	finished, err := pubSvc.ReportResult(ctx, accID, devID, reqID, publish.ResultInput{
		Outcome:  "succeeded",
		RemoteID: "issue-8888",
		URL:      "https://gitee.com/ent/issues/8888",
	})
	require.NoError(t, err)
	require.Equal(t, publish.StateSucceeded, finished.State)
	require.Equal(t, "issue-8888", finished.RemoteID)

	// Verify gitee_link entity was created in sync_entities
	linkID := publish.LinkEntityID("task-pub-1", "ent:1001")
	linkEnt, err := syncSvc.GetEntity(ctx, pool, accID, string(sync.TypeGiteeLink), linkID)
	require.NoError(t, err)
	require.NotNil(t, linkEnt)
	require.Equal(t, "issue-8888", linkEnt.Fields["remote_id"])
	require.Equal(t, "https://gitee.com/ent/issues/8888", linkEnt.Fields["url"])
}
