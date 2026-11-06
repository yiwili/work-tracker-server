package history

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

type validItem struct {
	id         string
	title      string
	details    string
	project    string
	workDate   any
	source     string
	sourceID   string
	url        string
	importedAt time.Time
}

func (s *Service) ImportBatch(ctx context.Context, accountID uuid.UUID, input ImportHistoryInput) (ImportBatchResult, error) {
	if len(input.Items) == 0 {
		return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", "items cannot be empty")
	}
	if len(input.Items) > 500 {
		return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", "items cannot exceed 500 per batch")
	}

	validItems := make([]validItem, len(input.Items))
	for idx, it := range input.Items {
		id := strings.TrimSpace(it.ID)
		if id == "" || len(id) > 64 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: invalid id (must be 1..64 chars)", idx))
		}
		title := strings.TrimSpace(it.Title)
		if title == "" || len(title) > 500 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: title cannot be empty or exceed 500 chars", idx))
		}
		if len(it.Details) > 20000 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: details cannot exceed 20000 chars", idx))
		}
		if len(it.Project) > 500 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: project cannot exceed 500 chars", idx))
		}
		source := strings.TrimSpace(it.Source)
		if source == "" {
			source = "file"
		}
		if len(source) > 32 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: source cannot exceed 32 chars", idx))
		}
		if len(it.SourceID) > 200 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: source_id cannot exceed 200 chars", idx))
		}
		if len(it.URL) > 2000 {
			return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: url cannot exceed 2000 chars", idx))
		}

		var workDateVal any
		if it.WorkDate != nil && strings.TrimSpace(*it.WorkDate) != "" {
			parsedDate, err := time.Parse("2006-01-02", strings.TrimSpace(*it.WorkDate))
			if err != nil {
				return ImportBatchResult{}, httpx.E(http.StatusBadRequest, "validation_failed", fmt.Sprintf("item[%d]: invalid work_date format (must be YYYY-MM-DD): %v", idx, err))
			}
			workDateVal = parsedDate
		}

		importedAt := time.Now()
		if it.ImportedAt != nil && strings.TrimSpace(*it.ImportedAt) != "" {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*it.ImportedAt)); err == nil {
				importedAt = parsed
			}
		}

		validItems[idx] = validItem{
			id:         id,
			title:      title,
			details:    it.Details,
			project:    it.Project,
			workDate:   workDateVal,
			source:     source,
			sourceID:   it.SourceID,
			url:        it.URL,
			importedAt: importedAt,
		}
	}

	if s.pool == nil {
		return ImportBatchResult{}, fmt.Errorf("database pool is not initialized")
	}

	result := ImportBatchResult{
		BatchIndex: input.BatchIndex,
		Received:   len(input.Items),
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImportBatchResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const upsertQuery = `
		WITH inserted AS (
			INSERT INTO history_work_items (
				account_id, id, title, details, project, work_date, source, source_id, url, imported_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), now())
			ON CONFLICT (account_id, id) DO UPDATE SET
				title = EXCLUDED.title,
				details = EXCLUDED.details,
				project = EXCLUDED.project,
				work_date = EXCLUDED.work_date,
				source = EXCLUDED.source,
				source_id = EXCLUDED.source_id,
				url = EXCLUDED.url,
				updated_at = now()
			WHERE history_work_items.title IS DISTINCT FROM EXCLUDED.title
			   OR history_work_items.details IS DISTINCT FROM EXCLUDED.details
			   OR history_work_items.project IS DISTINCT FROM EXCLUDED.project
			   OR history_work_items.work_date IS DISTINCT FROM EXCLUDED.work_date
			   OR history_work_items.source IS DISTINCT FROM EXCLUDED.source
			   OR history_work_items.source_id IS DISTINCT FROM EXCLUDED.source_id
			   OR history_work_items.url IS DISTINCT FROM EXCLUDED.url
			RETURNING (xmax = 0) AS is_insert
		)
		SELECT is_insert FROM inserted;
	`

	for _, it := range validItems {
		var isInsert bool
		err := tx.QueryRow(ctx, upsertQuery,
			accountID,
			it.id,
			it.title,
			it.details,
			it.project,
			it.workDate,
			it.source,
			it.sourceID,
			it.url,
			it.importedAt,
		).Scan(&isInsert)

		if err != nil {
			if err == pgx.ErrNoRows {
				result.Unchanged++
			} else {
				return ImportBatchResult{}, fmt.Errorf("execute upsert for item %s: %w", it.id, err)
			}
		} else {
			if isInsert {
				result.Inserted++
			} else {
				result.Updated++
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ImportBatchResult{}, fmt.Errorf("commit tx: %w", err)
	}

	return result, nil
}

func (s *Service) List(ctx context.Context, accountID uuid.UUID, filter ListFilter) (ListResult, error) {
	if s.pool == nil {
		return ListResult{}, fmt.Errorf("database pool is not initialized")
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	limit := filter.Limit
	if limit < 1 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	var whereClauses []string
	var args []any
	argIdx := 1

	whereClauses = append(whereClauses, fmt.Sprintf("account_id = $%d", argIdx))
	args = append(args, accountID)
	argIdx++

	if filter.DateFrom != "" {
		if _, err := time.Parse("2006-01-02", filter.DateFrom); err != nil {
			return ListResult{}, httpx.E(http.StatusBadRequest, "validation_failed", "date_from must be YYYY-MM-DD")
		}
		whereClauses = append(whereClauses, fmt.Sprintf("work_date >= $%d", argIdx))
		args = append(args, filter.DateFrom)
		argIdx++
	}

	if filter.DateTo != "" {
		if _, err := time.Parse("2006-01-02", filter.DateTo); err != nil {
			return ListResult{}, httpx.E(http.StatusBadRequest, "validation_failed", "date_to must be YYYY-MM-DD")
		}
		whereClauses = append(whereClauses, fmt.Sprintf("work_date <= $%d", argIdx))
		args = append(args, filter.DateTo)
		argIdx++
	}

	if filter.HasDate != nil {
		if *filter.HasDate {
			whereClauses = append(whereClauses, "work_date IS NOT NULL")
		} else {
			whereClauses = append(whereClauses, "work_date IS NULL")
		}
	}

	if filter.Project != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("project = $%d", argIdx))
		args = append(args, filter.Project)
		argIdx++
	}

	if filter.Source != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("source = $%d", argIdx))
		args = append(args, filter.Source)
		argIdx++
	}

	if filter.Q != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("title ILIKE $%d", argIdx))
		args = append(args, "%"+filter.Q+"%")
		argIdx++
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM history_work_items WHERE %s", whereSQL)
	var total int64
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count history items: %w", err)
	}

	dataQuery := fmt.Sprintf(`
		SELECT id, title, details, project, to_char(work_date, 'YYYY-MM-DD'), source, source_id, url, imported_at, created_at, updated_at
		FROM history_work_items
		WHERE %s
		ORDER BY work_date DESC NULLS LAST, id ASC
		LIMIT $%d OFFSET $%d
	`, whereSQL, argIdx, argIdx+1)

	queryArgs := append(args, limit, offset)
	rows, err := s.pool.Query(ctx, dataQuery, queryArgs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("query history items: %w", err)
	}
	defer rows.Close()

	items := make([]HistoryWorkItem, 0, limit)
	for rows.Next() {
		var it HistoryWorkItem
		var workDateStr *string
		if err := rows.Scan(
			&it.ID,
			&it.Title,
			&it.Details,
			&it.Project,
			&workDateStr,
			&it.Source,
			&it.SourceID,
			&it.URL,
			&it.ImportedAt,
			&it.CreatedAt,
			&it.UpdatedAt,
		); err != nil {
			return ListResult{}, fmt.Errorf("scan history item: %w", err)
		}
		it.WorkDate = workDateStr
		items = append(items, it)
	}

	return ListResult{
		Total: total,
		Page:  page,
		Limit: limit,
		Items: items,
	}, nil
}

func (s *Service) Summary(ctx context.Context, accountID uuid.UUID) (SummaryResult, error) {
	if s.pool == nil {
		return SummaryResult{}, fmt.Errorf("database pool is not initialized")
	}

	summary := SummaryResult{
		Sources: make(map[string]int64),
	}

	const mainStatsQuery = `
		SELECT COUNT(*),
		       to_char(MIN(work_date), 'YYYY-MM-DD'),
		       to_char(MAX(work_date), 'YYYY-MM-DD'),
		       COUNT(*) FILTER (WHERE work_date IS NULL)
		FROM history_work_items
		WHERE account_id = $1
	`

	var earliest, latest *string
	if err := s.pool.QueryRow(ctx, mainStatsQuery, accountID).Scan(
		&summary.TotalRecords,
		&earliest,
		&latest,
		&summary.MissingDateCount,
	); err != nil {
		return SummaryResult{}, fmt.Errorf("query history summary: %w", err)
	}

	summary.EarliestDate = earliest
	summary.LatestDate = latest

	const sourceStatsQuery = `
		SELECT source, COUNT(*)
		FROM history_work_items
		WHERE account_id = $1
		GROUP BY source
	`
	rows, err := s.pool.Query(ctx, sourceStatsQuery, accountID)
	if err != nil {
		return SummaryResult{}, fmt.Errorf("query source summary: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var src string
		var count int64
		if err := rows.Scan(&src, &count); err != nil {
			return SummaryResult{}, fmt.Errorf("scan source summary: %w", err)
		}
		summary.Sources[src] = count
	}

	return summary, nil
}
