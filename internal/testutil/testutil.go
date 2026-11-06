package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yiwili/work-tracker-server/internal/config"
	"github.com/yiwili/work-tracker-server/internal/db"
)

func NewTestConfig() config.Config {
	return config.Config{
		HTTPAddr:            ":0",
		DBMaxConns:          4,
		AllowRegistration:   true,
		CloudPublishEnabled: true,
		ChangeRetentionDays: 90,
		PublishPreviewTTL:   15 * time.Minute,
		SendingStaleAfter:   10 * time.Minute,
		SweepInterval:       1 * time.Minute,
		BcryptCost:          4, // fast for tests
		LogLevel:            "error",
		MaxBodyBytes:        4194304,
	}
}

func NewDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
		return nil
	}

	ctx := context.Background()

	// Connect to root DB to create throwaway schema
	rootPool, err := db.Open(ctx, url, 2, "")
	if err != nil {
		t.Skipf("cannot connect to TEST_DATABASE_URL: %v; skipping", err)
		return nil
	}
	defer rootPool.Close()

	randomBytes := make([]byte, 6)
	_, _ = rand.Read(randomBytes)
	schemaName := "t_" + hex.EncodeToString(randomBytes)

	_, err = rootPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", schemaName))
	if err != nil {
		t.Fatalf("failed to create test schema: %v", err)
	}

	pool, err := db.Open(ctx, url, 4, schemaName)
	if err != nil {
		t.Fatalf("failed to open pool for test schema: %v", err)
	}

	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("failed to run migrations in test schema: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupPool, err := db.Open(ctx, url, 2, "")
		if err == nil {
			_, _ = cleanupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE", schemaName))
			cleanupPool.Close()
		}
	})

	return pool
}
