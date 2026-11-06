package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Job struct {
	ID          int64           `json:"id"`
	AccountID   *uuid.UUID      `json:"account_id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	State       string          `json:"state"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	RunAfter    time.Time       `json:"run_after"`
	LockedBy    string          `json:"locked_by"`
	LockedUntil *time.Time      `json:"locked_until"`
	LastError   string          `json:"last_error"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type Queue struct {
	pool          *pgxpool.Pool
	OutboxSubject string
}

func NewQueue(pool *pgxpool.Pool, outboxSubject string) *Queue {
	return &Queue{
		pool:          pool,
		OutboxSubject: outboxSubject,
	}
}

func (q *Queue) Enqueue(ctx context.Context, tx pgx.Tx, accountID *uuid.UUID, kind string, payload any, runAfter time.Time) (int64, error) {
	bytes, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal job payload: %w", err)
	}

	if runAfter.IsZero() {
		runAfter = time.Now()
	}

	var jobID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO jobs (account_id, kind, payload, run_after)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, accountID, kind, bytes, runAfter).Scan(&jobID)
	if err != nil {
		return 0, fmt.Errorf("insert job: %w", err)
	}

	if q.OutboxSubject != "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO job_outbox (job_id, subject)
			VALUES ($1, $2)
		`, jobID, q.OutboxSubject)
		if err != nil {
			return 0, fmt.Errorf("insert job outbox: %w", err)
		}
	}

	return jobID, nil
}

func (q *Queue) Claim(ctx context.Context, workerID string, kinds []string, lease time.Duration) (*Job, error) {
	now := time.Now()
	lockedUntil := now.Add(lease)

	// Single atomic query with UPDATE ... FROM subquery
	var j Job
	query := `
		WITH ready AS (
			SELECT id
			FROM jobs
			WHERE (
				(state = 'queued' AND run_after <= $1)
				OR (state = 'running' AND locked_until < $1)
			)
			AND (cardinality($2::text[]) = 0 OR kind = ANY($2::text[]))
			ORDER BY run_after ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE jobs
		SET state = 'running',
		    locked_by = $3,
		    locked_until = $4,
		    attempts = attempts + 1,
		    updated_at = $1
		FROM ready
		WHERE jobs.id = ready.id
		RETURNING jobs.id, jobs.account_id, jobs.kind, jobs.payload, jobs.state,
		          jobs.attempts, jobs.max_attempts, jobs.run_after, jobs.locked_by,
		          jobs.locked_until, jobs.last_error, jobs.created_at, jobs.updated_at
	`

	row := q.pool.QueryRow(ctx, query, now, kinds, workerID, lockedUntil)
	err := row.Scan(&j.ID, &j.AccountID, &j.Kind, &j.Payload, &j.State, &j.Attempts,
		&j.MaxAttempts, &j.RunAfter, &j.LockedBy, &j.LockedUntil, &j.LastError, &j.CreatedAt, &j.UpdatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // No jobs ready
		}
		return nil, fmt.Errorf("claim job: %w", err)
	}

	return &j, nil
}

func (q *Queue) Complete(ctx context.Context, jobID int64) error {
	now := time.Now()
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET state = 'done',
		    locked_by = '',
		    locked_until = NULL,
		    updated_at = $1
		WHERE id = $2
	`, now, jobID)
	return err
}

func (q *Queue) Fail(ctx context.Context, jobID int64, errText string, retryAfter time.Duration) error {
	now := time.Now()

	// Read current attempts and max_attempts
	var attempts, maxAttempts int
	err := q.pool.QueryRow(ctx, "SELECT attempts, max_attempts FROM jobs WHERE id = $1", jobID).Scan(&attempts, &maxAttempts)
	if err != nil {
		return err
	}

	state := "queued"
	runAfter := now.Add(retryAfter)

	if attempts >= maxAttempts {
		state = "failed"
	}

	_, err = q.pool.Exec(ctx, `
		UPDATE jobs
		SET state = $1,
		    run_after = $2,
		    locked_by = '',
		    locked_until = NULL,
		    last_error = $3,
		    updated_at = $4
		WHERE id = $5
	`, state, runAfter, errText, now, jobID)

	return err
}
