package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/db"
	"github.com/yiwili/work-tracker-server/internal/jobs"
	"github.com/yiwili/work-tracker-server/internal/testutil"
)

func TestJobQueueExclusivity(t *testing.T) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return
	}

	ctx := context.Background()
	q := jobs.NewQueue(pool, "test.subject")

	// Enqueue in tx
	var jobID int64
	err := db.InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		jobID, err = q.Enqueue(ctx, tx, nil, "test_job", map[string]string{"foo": "bar"}, time.Now())
		return err
	})
	require.NoError(t, err)
	require.Greater(t, jobID, int64(0))

	// Claim worker 1
	j1, err := q.Claim(ctx, "worker-1", []string{"test_job"}, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, j1)
	require.Equal(t, jobID, j1.ID)

	// Concurrent claim worker 2 should get nothing (job locked)
	j2, err := q.Claim(ctx, "worker-2", []string{"test_job"}, 5*time.Second)
	require.NoError(t, err)
	require.Nil(t, j2)

	// Complete job
	err = q.Complete(ctx, j1.ID)
	require.NoError(t, err)

	// Verify job is done
	var state string
	err = pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id = $1", j1.ID).Scan(&state)
	require.NoError(t, err)
	require.Equal(t, "done", state)
}
