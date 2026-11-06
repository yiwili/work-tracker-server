package history_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/yiwili/work-tracker-server/internal/history"
	"github.com/yiwili/work-tracker-server/internal/testutil"
)

func TestHistoryValidation(t *testing.T) {
	svc := history.NewService(nil)
	ctx := context.Background()
	accID := uuid.New()

	// 1. Empty items rejected
	_, err := svc.ImportBatch(ctx, accID, history.ImportHistoryInput{
		BatchIndex: 1,
		Items:      []history.ImportItemInput{},
	})
	require.Error(t, err)

	// 2. Empty title rejected
	_, err = svc.ImportBatch(ctx, accID, history.ImportHistoryInput{
		BatchIndex: 1,
		Items: []history.ImportItemInput{
			{ID: "h1", Title: "   "},
		},
	})
	require.Error(t, err)

	// 3. Invalid date format rejected
	badDate := "2026/10/20"
	_, err = svc.ImportBatch(ctx, accID, history.ImportHistoryInput{
		BatchIndex: 1,
		Items: []history.ImportItemInput{
			{ID: "h1", Title: "Valid Title", WorkDate: &badDate},
		},
	})
	require.Error(t, err)
}

func TestHistoryLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	if pool == nil {
		return
	}

	ctx := context.Background()
	svc := history.NewService(pool)

	accA := uuid.New()
	accB := uuid.New()

	_, err := pool.Exec(ctx, "INSERT INTO accounts (id, email, password_hash) VALUES ($1, 'h_user_a@example.com', 'hash')", accA)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO accounts (id, email, password_hash) VALUES ($1, 'h_user_b@example.com', 'hash')", accB)
	require.NoError(t, err)

	date1 := "2026-10-20"
	date2 := "2026-10-25"

	// 1. Account A imports batch of 3 items (including one with missing date)
	resA, err := svc.ImportBatch(ctx, accA, history.ImportHistoryInput{
		BatchIndex:   1,
		TotalBatches: 1,
		Items: []history.ImportItemInput{
			{
				ID:       "id-1",
				Title:    "Feature A",
				Project:  "project-alpha",
				WorkDate: &date1,
				Source:   "gitee",
				SourceID: "101",
			},
			{
				ID:       "id-2",
				Title:    "Feature B",
				Project:  "project-alpha",
				WorkDate: &date2,
				Source:   "file",
			},
			{
				ID:       "id-3",
				Title:    "Legacy Prototype with missing date",
				Project:  "project-beta",
				WorkDate: nil, // Missing date
				Source:   "file",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 3, resA.Received)
	require.Equal(t, 3, resA.Inserted)
	require.Equal(t, 0, resA.Updated)
	require.Equal(t, 0, resA.Unchanged)

	// 2. Re-import identical batch -> All Unchanged (idempotency)
	resA2, err := svc.ImportBatch(ctx, accA, history.ImportHistoryInput{
		BatchIndex:   1,
		TotalBatches: 1,
		Items: []history.ImportItemInput{
			{
				ID:       "id-1",
				Title:    "Feature A",
				Project:  "project-alpha",
				WorkDate: &date1,
				Source:   "gitee",
				SourceID: "101",
			},
			{
				ID:       "id-2",
				Title:    "Feature B",
				Project:  "project-alpha",
				WorkDate: &date2,
				Source:   "file",
			},
			{
				ID:       "id-3",
				Title:    "Legacy Prototype with missing date",
				Project:  "project-beta",
				WorkDate: nil,
				Source:   "file",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 0, resA2.Inserted)
	require.Equal(t, 0, resA2.Updated)
	require.Equal(t, 3, resA2.Unchanged)

	// 3. Update one item -> 1 Updated, 2 Unchanged
	resA3, err := svc.ImportBatch(ctx, accA, history.ImportHistoryInput{
		BatchIndex:   1,
		TotalBatches: 1,
		Items: []history.ImportItemInput{
			{
				ID:       "id-1",
				Title:    "Feature A (Modified Title)",
				Project:  "project-alpha",
				WorkDate: &date1,
				Source:   "gitee",
				SourceID: "101",
			},
			{
				ID:       "id-2",
				Title:    "Feature B",
				Project:  "project-alpha",
				WorkDate: &date2,
				Source:   "file",
			},
			{
				ID:       "id-3",
				Title:    "Legacy Prototype with missing date",
				Project:  "project-beta",
				WorkDate: nil,
				Source:   "file",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 0, resA3.Inserted)
	require.Equal(t, 1, resA3.Updated)
	require.Equal(t, 2, resA3.Unchanged)

	// 4. List query with sorting: date descending, missing date is NULLS LAST
	listA, err := svc.List(ctx, accA, history.ListFilter{Page: 1, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(3), listA.Total)
	require.Len(t, listA.Items, 3)
	require.Equal(t, "id-2", listA.Items[0].ID) // 2026-10-25
	require.Equal(t, "id-1", listA.Items[1].ID) // 2026-10-20
	require.Equal(t, "id-3", listA.Items[2].ID) // NULL date last
	require.Nil(t, listA.Items[2].WorkDate)

	// 5. Cross-account isolation: Account B has zero items
	listB, err := svc.List(ctx, accB, history.ListFilter{Page: 1, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(0), listB.Total)
	require.Empty(t, listB.Items)

	// Account B imports item with same ID "id-1" but different title
	bDate := "2026-11-01"
	resB, err := svc.ImportBatch(ctx, accB, history.ImportHistoryInput{
		BatchIndex: 1,
		Items: []history.ImportItemInput{
			{
				ID:       "id-1",
				Title:    "Account B's Own Task",
				Project:  "project-gamma",
				WorkDate: &bDate,
				Source:   "custom",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, resB.Inserted)

	// Account A's "id-1" must remain intact
	listAAfter, err := svc.List(ctx, accA, history.ListFilter{Page: 1, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(3), listAAfter.Total)
	require.Equal(t, "Feature A (Modified Title)", listAAfter.Items[1].Title)

	// 6. Filter by project
	listProject, err := svc.List(ctx, accA, history.ListFilter{Project: "project-alpha"})
	require.NoError(t, err)
	require.Equal(t, int64(2), listProject.Total)

	// 7. Summary
	summaryA, err := svc.Summary(ctx, accA)
	require.NoError(t, err)
	require.Equal(t, int64(3), summaryA.TotalRecords)
	require.Equal(t, int64(1), summaryA.MissingDateCount)
	require.Equal(t, "2026-10-20", *summaryA.EarliestDate)
	require.Equal(t, "2026-10-25", *summaryA.LatestDate)
	require.Equal(t, int64(1), summaryA.Sources["gitee"])
	require.Equal(t, int64(2), summaryA.Sources["file"])
}
