package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/db"
	"github.com/yiwili/work-tracker-server/internal/testutil"
)

func TestMigrationsIdempotent(t *testing.T) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return
	}

	ctx := context.Background()
	// Migrate already ran in testutil.NewDB; running it a second time should be a clean no-op
	err := db.Migrate(ctx, pool)
	require.NoError(t, err)
}

func TestInTxRollback(t *testing.T) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return
	}

	ctx := context.Background()

	// Transaction that returns error rolls back
	err := db.InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "CREATE TABLE temp_rollback_test (id int)")
		require.NoError(t, err)
		return errors.New("abort intentionally")
	})
	require.Error(t, err)

	var exists bool
	err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'temp_rollback_test')").Scan(&exists)
	require.NoError(t, err)
	require.False(t, exists)
}
