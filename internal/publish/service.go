package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yiwili/work-tracker-server/internal/config"
	"github.com/yiwili/work-tracker-server/internal/db"
	"github.com/yiwili/work-tracker-server/internal/httpx"
	"github.com/yiwili/work-tracker-server/internal/sync"
)

type Service struct {
	pool        *pgxpool.Pool
	cfg         config.Config
	entityStore EntityStore
	nowFn       func() time.Time
}

func NewService(pool *pgxpool.Pool, cfg config.Config, entityStore EntityStore) *Service {
	return &Service{
		pool:        pool,
		cfg:         cfg,
		entityStore: entityStore,
		nowFn:       time.Now,
	}
}

func (s *Service) SetNow(fn func() time.Time) {
	s.nowFn = fn
}

func LinkEntityID(workItemID, targetScope string) string {
	h := sha256.Sum256([]byte(workItemID + "\n" + targetScope))
	return "lnk_" + hex.EncodeToString(h[:16])
}

func (s *Service) RegisterBatch(ctx context.Context, accountID, deviceID uuid.UUID, input RegisterBatchInput) (PublishBatch, int, error) {
	if len(input.Items) == 0 || len(input.Items) > 200 {
		return PublishBatch{}, 0, httpx.E(http.StatusBadRequest, "validation_failed", "items must contain 1..200 requests")
	}

	if input.Executor == "cloud" && !s.cfg.CloudPublishEnabled {
		return PublishBatch{}, 0, httpx.E(http.StatusForbidden, "cloud_publish_disabled", "cloud publish executor is not enabled")
	}

	now := s.nowFn()
	var batch PublishBatch
	var statusCode = http.StatusCreated

	err := db.InTx(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := s.entityStore.LockAccount(ctx, tx, accountID); err != nil {
			return err
		}

		// 1. Check idempotency
		var existingID uuid.UUID
		var existingCreated time.Time
		err := tx.QueryRow(ctx, `
			SELECT id, created_at FROM publish_batches
			WHERE account_id = $1 AND idempotency_key = $2
		`, accountID, input.IdempotencyKey).Scan(&existingID, &existingCreated)

		if err == nil {
			// Found existing batch: return it
			statusCode = http.StatusOK
			b, err := s.getBatchInTx(ctx, tx, accountID, existingID)
			if err != nil {
				return err
			}
			batch = b
			return nil
		}

		// 2. Check unified publish enabled
		var unifiedPublishEnabled bool
		err = tx.QueryRow(ctx, "SELECT unified_publish_enabled FROM accounts WHERE id = $1", accountID).Scan(&unifiedPublishEnabled)
		if err != nil {
			return err
		}
		if !unifiedPublishEnabled {
			return httpx.E(http.StatusConflict, "migration_incomplete", "unified publish is not enabled; please complete migration first")
		}

		// 3. Expire old registered requests
		_, _ = tx.Exec(ctx, `
			UPDATE publish_requests
			SET state = 'expired', updated_at = $1
			WHERE account_id = $2 AND state = 'registered' AND expires_at < $1
		`, now, accountID)

		// 4. Validate items & preview freshness
		batchID := uuid.New()
		expiresAt := now.Add(s.cfg.PublishPreviewTTL)
		var requests []PublishRequest

		for _, item := range input.Items {
			// Check work item exists, not deleted, version matches
			wi, err := s.entityStore.GetEntity(ctx, tx, accountID, string(sync.TypeWorkItem), item.WorkItemID)
			if err != nil {
				return err
			}
			if wi == nil {
				return httpx.E(http.StatusConflict, "preview_stale", fmt.Sprintf("work item '%s' not found", item.WorkItemID))
			}
			deleted, _ := wi.Fields["deleted"].(bool)
			if deleted {
				return httpx.E(http.StatusConflict, "preview_stale", fmt.Sprintf("work item '%s' is deleted", item.WorkItemID))
			}
			if wi.Version != item.WorkItemVersion {
				return httpx.E(http.StatusConflict, "preview_stale", fmt.Sprintf("work item '%s' version changed: expected %d, got %d", item.WorkItemID, item.WorkItemVersion, wi.Version))
			}

			// Check config versions
			for cfgKey, expectedVer := range item.ConfigVersions {
				parts := strings.SplitN(cfgKey, ":", 2)
				if len(parts) == 2 {
					cfgEnt, err := s.entityStore.GetEntity(ctx, tx, accountID, parts[0], parts[1])
					if err != nil {
						return err
					}
					if cfgEnt == nil || cfgEnt.Version != int64(expectedVer) {
						return httpx.E(http.StatusConflict, "preview_stale", fmt.Sprintf("config '%s' version changed", cfgKey))
					}
				}
			}

			// Check existing gitee_link
			linkID := LinkEntityID(item.WorkItemID, item.TargetScope)
			linkEnt, err := s.entityStore.GetEntity(ctx, tx, accountID, string(sync.TypeGiteeLink), linkID)
			if err != nil {
				return err
			}

			op := item.Operation
			remoteID := item.RemoteID
			opAdjusted := false

			if linkEnt != nil {
				linkDeleted, _ := linkEnt.Fields["deleted"].(bool)
				if !linkDeleted {
					if rid, ok := linkEnt.Fields["remote_id"].(string); ok && rid != "" {
						op = "update"
						remoteID = rid
						opAdjusted = true
					}
				}
			}

			reqID := uuid.New()
			targetJSON, _ := json.Marshal(item.Target)
			payloadJSON, _ := json.Marshal(item.Payload)
			cfgVersJSON, _ := json.Marshal(item.ConfigVersions)

			_, err = tx.Exec(ctx, `
				INSERT INTO publish_requests (
					id, account_id, batch_id, work_item_id, work_item_version, target_scope,
					operation, operation_adjusted, remote_id, url, target, payload,
					config_versions, executor, state, expires_at, created_at, updated_at
				)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $17)
			`, reqID, accountID, batchID, item.WorkItemID, item.WorkItemVersion, item.TargetScope,
				op, opAdjusted, remoteID, "", targetJSON, payloadJSON,
				cfgVersJSON, input.Executor, StateRegistered, expiresAt, now)

			if err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23505" {
					return httpx.E(http.StatusConflict, "in_flight", fmt.Sprintf("work item '%s' already has a publication in progress for target '%s'", item.WorkItemID, item.TargetScope))
				}
				return fmt.Errorf("insert publish request: %w", err)
			}

			requests = append(requests, PublishRequest{
				ID:                reqID,
				BatchID:           &batchID,
				WorkItemID:        item.WorkItemID,
				WorkItemVersion:   item.WorkItemVersion,
				TargetScope:       item.TargetScope,
				Operation:         op,
				OperationAdjusted: opAdjusted,
				RemoteID:          remoteID,
				URL:               "",
				Target:            item.Target,
				Payload:           item.Payload,
				ConfigVersions:    item.ConfigVersions,
				Executor:          input.Executor,
				State:             StateRegistered,
				ExpiresAt:         &expiresAt,
				CreatedAt:         now,
				UpdatedAt:         now,
			})
		}

		// Insert batch
		_, err = tx.Exec(ctx, `
			INSERT INTO publish_batches (id, account_id, idempotency_key, executor, created_by_device, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, batchID, accountID, input.IdempotencyKey, input.Executor, deviceID, now)
		if err != nil {
			return fmt.Errorf("insert batch: %w", err)
		}

		batch = PublishBatch{
			ID:              batchID,
			IdempotencyKey:  input.IdempotencyKey,
			Executor:        input.Executor,
			CreatedByDevice: &deviceID,
			CreatedAt:       now,
			Requests:        requests,
		}

		return nil
	})

	if err != nil {
		return PublishBatch{}, 0, err
	}

	return batch, statusCode, nil
}

func (s *Service) getBatchInTx(ctx context.Context, tx pgx.Tx, accountID, batchID uuid.UUID) (PublishBatch, error) {
	var b PublishBatch
	err := tx.QueryRow(ctx, `
		SELECT id, idempotency_key, executor, created_by_device, created_at
		FROM publish_batches
		WHERE id = $1 AND account_id = $2
	`, batchID, accountID).Scan(&b.ID, &b.IdempotencyKey, &b.Executor, &b.CreatedByDevice, &b.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublishBatch{}, httpx.E(http.StatusNotFound, "not_found", "batch not found")
		}
		return PublishBatch{}, err
	}

	reqs, err := s.listRequestsInTx(ctx, tx, accountID, &batchID, "", "", 200)
	if err != nil {
		return PublishBatch{}, err
	}
	b.Requests = reqs

	return b, nil
}

func (s *Service) GetBatch(ctx context.Context, accountID, batchID uuid.UUID) (PublishBatch, error) {
	var b PublishBatch
	err := db.InTx(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		val, err := s.getBatchInTx(ctx, tx, accountID, batchID)
		if err != nil {
			return err
		}
		b = val
		return nil
	})
	return b, err
}

func (s *Service) ListRequests(ctx context.Context, accountID uuid.UUID, state PublishState, workItemID string, limit int) ([]PublishRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.listRequestsInTx(ctx, s.pool, accountID, nil, state, workItemID, limit)
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Service) listRequestsInTx(ctx context.Context, q queryer, accountID uuid.UUID, batchID *uuid.UUID, state PublishState, workItemID string, limit int) ([]PublishRequest, error) {
	query := `
		SELECT id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
		       remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
		       error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
		FROM publish_requests
		WHERE account_id = $1
	`
	args := []any{accountID}

	if batchID != nil {
		args = append(args, *batchID)
		query += fmt.Sprintf(" AND batch_id = $%d", len(args))
	}
	if state != "" {
		args = append(args, state)
		query += fmt.Sprintf(" AND state = $%d", len(args))
	}
	if workItemID != "" {
		args = append(args, workItemID)
		query += fmt.Sprintf(" AND work_item_id = $%d", len(args))
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", limit)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []PublishRequest
	for rows.Next() {
		var r PublishRequest
		var targetJSON, payloadJSON, cfgJSON []byte
		err := rows.Scan(&r.ID, &r.BatchID, &r.WorkItemID, &r.WorkItemVersion, &r.TargetScope, &r.Operation,
			&r.OperationAdjusted, &r.RemoteID, &r.URL, &targetJSON, &payloadJSON, &cfgJSON, &r.Executor,
			&r.ExecutorDeviceID, &r.State, &r.Error, &r.Note, &r.PublishedVersion, &r.ExpiresAt,
			&r.ClaimedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(targetJSON, &r.Target)
		_ = json.Unmarshal(payloadJSON, &r.Payload)
		_ = json.Unmarshal(cfgJSON, &r.ConfigVersions)
		reqs = append(reqs, r)
	}

	if reqs == nil {
		reqs = []PublishRequest{}
	}

	return reqs, nil
}

func (s *Service) Claim(ctx context.Context, accountID, deviceID, requestID uuid.UUID) (PublishRequest, error) {
	now := s.nowFn()

	var r PublishRequest
	var targetJSON, payloadJSON, cfgJSON []byte

	// 1. Try atomic claim
	err := s.pool.QueryRow(ctx, `
		UPDATE publish_requests
		SET state = 'sending',
		    executor_device_id = $1,
		    claimed_at = $2,
		    updated_at = $2
		WHERE id = $3 AND account_id = $4 AND state = 'registered' AND expires_at > $2
		RETURNING id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
		          remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
		          error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
	`, deviceID, now, requestID, accountID).Scan(
		&r.ID, &r.BatchID, &r.WorkItemID, &r.WorkItemVersion, &r.TargetScope, &r.Operation,
		&r.OperationAdjusted, &r.RemoteID, &r.URL, &targetJSON, &payloadJSON, &cfgJSON, &r.Executor,
		&r.ExecutorDeviceID, &r.State, &r.Error, &r.Note, &r.PublishedVersion, &r.ExpiresAt,
		&r.ClaimedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt,
	)

	if err == nil {
		_ = json.Unmarshal(targetJSON, &r.Target)
		_ = json.Unmarshal(payloadJSON, &r.Payload)
		_ = json.Unmarshal(cfgJSON, &r.ConfigVersions)
		return r, nil
	}

	// 2. Not updated: re-read to diagnose state
	err = s.pool.QueryRow(ctx, `
		SELECT id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
		       remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
		       error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
		FROM publish_requests
		WHERE id = $1 AND account_id = $2
	`, requestID, accountID).Scan(
		&r.ID, &r.BatchID, &r.WorkItemID, &r.WorkItemVersion, &r.TargetScope, &r.Operation,
		&r.OperationAdjusted, &r.RemoteID, &r.URL, &targetJSON, &payloadJSON, &cfgJSON, &r.Executor,
		&r.ExecutorDeviceID, &r.State, &r.Error, &r.Note, &r.PublishedVersion, &r.ExpiresAt,
		&r.ClaimedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublishRequest{}, httpx.E(http.StatusNotFound, "not_found", "publish request not found")
		}
		return PublishRequest{}, err
	}

	_ = json.Unmarshal(targetJSON, &r.Target)
	_ = json.Unmarshal(payloadJSON, &r.Payload)
	_ = json.Unmarshal(cfgJSON, &r.ConfigVersions)

	// Idempotent retry by the same device
	if r.State == StateSending && r.ExecutorDeviceID != nil && *r.ExecutorDeviceID == deviceID {
		return r, nil
	}

	if r.State == StateRegistered && r.ExpiresAt != nil && r.ExpiresAt.Before(now) {
		_, _ = s.pool.Exec(ctx, "UPDATE publish_requests SET state='expired', updated_at=$1 WHERE id=$2", now, requestID)
		return PublishRequest{}, httpx.E(http.StatusGone, "request_expired", "publish request has expired")
	}

	return PublishRequest{}, httpx.E(http.StatusConflict, "invalid_state", fmt.Sprintf("cannot claim request in state '%s'", r.State))
}

func (s *Service) ReportResult(ctx context.Context, accountID, deviceID, requestID uuid.UUID, input ResultInput) (PublishRequest, error) {
	now := s.nowFn()

	var r PublishRequest
	err := db.InTx(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := s.entityStore.LockAccount(ctx, tx, accountID); err != nil {
			return err
		}

		reqs, err := s.listRequestsInTx(ctx, tx, accountID, nil, "", "", 1)
		if err != nil {
			return err
		}
		_ = reqs

		// Read current
		var cur PublishRequest
		var targetJSON, payloadJSON, cfgJSON []byte
		err = tx.QueryRow(ctx, `
			SELECT id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
			       remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
			       error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
			FROM publish_requests
			WHERE id = $1 AND account_id = $2
		`, requestID, accountID).Scan(
			&cur.ID, &cur.BatchID, &cur.WorkItemID, &cur.WorkItemVersion, &cur.TargetScope, &cur.Operation,
			&cur.OperationAdjusted, &cur.RemoteID, &cur.URL, &targetJSON, &payloadJSON, &cfgJSON, &cur.Executor,
			&cur.ExecutorDeviceID, &cur.State, &cur.Error, &cur.Note, &cur.PublishedVersion, &cur.ExpiresAt,
			&cur.ClaimedAt, &cur.FinishedAt, &cur.CreatedAt, &cur.UpdatedAt,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.E(http.StatusNotFound, "not_found", "publish request not found")
			}
			return err
		}

		_ = json.Unmarshal(targetJSON, &cur.Target)
		_ = json.Unmarshal(payloadJSON, &cur.Payload)
		_ = json.Unmarshal(cfgJSON, &cur.ConfigVersions)

		// Repeating same final outcome is idempotent
		if string(cur.State) == input.Outcome {
			r = cur
			return nil
		}

		if cur.State != StateSending {
			return httpx.E(http.StatusConflict, "invalid_state", fmt.Sprintf("cannot report result for request in state '%s'", cur.State))
		}

		if cur.ExecutorDeviceID == nil || *cur.ExecutorDeviceID != deviceID {
			return httpx.E(http.StatusForbidden, "not_executor", "only the claiming device can report the result")
		}

		var newState PublishState
		var pubVer *int64
		switch input.Outcome {
		case "succeeded":
			if input.RemoteID == "" {
				return httpx.E(http.StatusBadRequest, "validation_failed", "remote_id is required on success")
			}
			newState = StateSucceeded
			pubVer = &cur.WorkItemVersion

			// Upsert gitee_link entity
			linkID := LinkEntityID(cur.WorkItemID, cur.TargetScope)
			_, err = s.entityStore.PutSystemEntity(ctx, tx, accountID, &deviceID, string(sync.TypeGiteeLink), linkID, map[string]any{
				"work_item_id":      cur.WorkItemID,
				"target_scope":      cur.TargetScope,
				"remote_id":         input.RemoteID,
				"url":               input.URL,
				"published_version": cur.WorkItemVersion,
				"published_at":      now.Format(time.RFC3339),
				"deleted":           false,
			})
			if err != nil {
				return fmt.Errorf("upsert gitee_link: %w", err)
			}

		case "failed":
			newState = StateFailed
		case "uncertain":
			newState = StateUncertain
		default:
			return httpx.E(http.StatusBadRequest, "validation_failed", "invalid outcome")
		}

		err = tx.QueryRow(ctx, `
			UPDATE publish_requests
			SET state = $1,
			    remote_id = $2,
			    url = $3,
			    error = $4,
			    published_version = $5,
			    finished_at = $6,
			    updated_at = $6
			WHERE id = $7 AND account_id = $8
			RETURNING id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
			          remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
			          error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
		`, newState, input.RemoteID, input.URL, input.Error, pubVer, now, requestID, accountID).Scan(
			&r.ID, &r.BatchID, &r.WorkItemID, &r.WorkItemVersion, &r.TargetScope, &r.Operation,
			&r.OperationAdjusted, &r.RemoteID, &r.URL, &targetJSON, &payloadJSON, &cfgJSON, &r.Executor,
			&r.ExecutorDeviceID, &r.State, &r.Error, &r.Note, &r.PublishedVersion, &r.ExpiresAt,
			&r.ClaimedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return err
		}

		_ = json.Unmarshal(targetJSON, &r.Target)
		_ = json.Unmarshal(payloadJSON, &r.Payload)
		_ = json.Unmarshal(cfgJSON, &r.ConfigVersions)

		return nil
	})

	return r, err
}

func (s *Service) Reconcile(ctx context.Context, accountID, deviceID, requestID uuid.UUID, input ReconcileInput) (PublishRequest, error) {
	now := s.nowFn()
	var r PublishRequest

	err := db.InTx(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := s.entityStore.LockAccount(ctx, tx, accountID); err != nil {
			return err
		}

		var cur PublishRequest
		var targetJSON, payloadJSON, cfgJSON []byte
		err := tx.QueryRow(ctx, `
			SELECT id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
			       remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
			       error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
			FROM publish_requests
			WHERE id = $1 AND account_id = $2
		`, requestID, accountID).Scan(
			&cur.ID, &cur.BatchID, &cur.WorkItemID, &cur.WorkItemVersion, &cur.TargetScope, &cur.Operation,
			&cur.OperationAdjusted, &cur.RemoteID, &cur.URL, &targetJSON, &payloadJSON, &cfgJSON, &cur.Executor,
			&cur.ExecutorDeviceID, &cur.State, &cur.Error, &cur.Note, &cur.PublishedVersion, &cur.ExpiresAt,
			&cur.ClaimedAt, &cur.FinishedAt, &cur.CreatedAt, &cur.UpdatedAt,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.E(http.StatusNotFound, "not_found", "publish request not found")
			}
			return err
		}

		if cur.State != StateUncertain && cur.State != StateSending {
			return httpx.E(http.StatusConflict, "invalid_state", fmt.Sprintf("cannot reconcile request in state '%s'", cur.State))
		}

		newState := StateFailed
		var pubVer *int64

		if input.Found {
			if input.RemoteID == "" {
				return httpx.E(http.StatusBadRequest, "validation_failed", "remote_id required when found=true")
			}
			newState = StateSucceeded
			pubVer = &cur.WorkItemVersion

			linkID := LinkEntityID(cur.WorkItemID, cur.TargetScope)
			_, err = s.entityStore.PutSystemEntity(ctx, tx, accountID, &deviceID, string(sync.TypeGiteeLink), linkID, map[string]any{
				"work_item_id":      cur.WorkItemID,
				"target_scope":      cur.TargetScope,
				"remote_id":         input.RemoteID,
				"url":               input.URL,
				"published_version": cur.WorkItemVersion,
				"published_at":      now.Format(time.RFC3339),
				"deleted":           false,
			})
			if err != nil {
				return err
			}
		}

		err = tx.QueryRow(ctx, `
			UPDATE publish_requests
			SET state = $1,
			    remote_id = $2,
			    url = $3,
			    published_version = $4,
			    finished_at = $5,
			    updated_at = $5
			WHERE id = $6 AND account_id = $7
			RETURNING id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
			          remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
			          error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
		`, newState, input.RemoteID, input.URL, pubVer, now, requestID, accountID).Scan(
			&r.ID, &r.BatchID, &r.WorkItemID, &r.WorkItemVersion, &r.TargetScope, &r.Operation,
			&r.OperationAdjusted, &r.RemoteID, &r.URL, &targetJSON, &payloadJSON, &cfgJSON, &r.Executor,
			&r.ExecutorDeviceID, &r.State, &r.Error, &r.Note, &r.PublishedVersion, &r.ExpiresAt,
			&r.ClaimedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return err
		}

		_ = json.Unmarshal(targetJSON, &r.Target)
		_ = json.Unmarshal(payloadJSON, &r.Payload)
		_ = json.Unmarshal(cfgJSON, &r.ConfigVersions)

		return nil
	})

	return r, err
}

func (s *Service) Cancel(ctx context.Context, accountID, requestID uuid.UUID) (PublishRequest, error) {
	now := s.nowFn()
	var r PublishRequest
	var targetJSON, payloadJSON, cfgJSON []byte

	err := s.pool.QueryRow(ctx, `
		UPDATE publish_requests
		SET state = 'cancelled',
		    updated_at = $1
		WHERE id = $2 AND account_id = $3 AND state = 'registered'
		RETURNING id, batch_id, work_item_id, work_item_version, target_scope, operation, operation_adjusted,
		          remote_id, url, target, payload, config_versions, executor, executor_device_id, state,
		          error, note, published_version, expires_at, claimed_at, finished_at, created_at, updated_at
	`, now, requestID, accountID).Scan(
		&r.ID, &r.BatchID, &r.WorkItemID, &r.WorkItemVersion, &r.TargetScope, &r.Operation,
		&r.OperationAdjusted, &r.RemoteID, &r.URL, &targetJSON, &payloadJSON, &cfgJSON, &r.Executor,
		&r.ExecutorDeviceID, &r.State, &r.Error, &r.Note, &r.PublishedVersion, &r.ExpiresAt,
		&r.ClaimedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublishRequest{}, httpx.E(http.StatusConflict, "invalid_state", "can only cancel requests in 'registered' state")
		}
		return PublishRequest{}, err
	}

	_ = json.Unmarshal(targetJSON, &r.Target)
	_ = json.Unmarshal(payloadJSON, &r.Payload)
	_ = json.Unmarshal(cfgJSON, &r.ConfigVersions)

	return r, nil
}

// --- Migration Endpoints ---

func (s *Service) ImportLinks(ctx context.Context, accountID, deviceID uuid.UUID, input ImportLinksInput) (map[string]any, error) {
	now := s.nowFn()

	var linksUpserted, linksUnchanged, pendingCreated, pendingSkipped int
	var conflicts []map[string]any

	err := db.InTx(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := s.entityStore.LockAccount(ctx, tx, accountID); err != nil {
			return err
		}

		for _, item := range input.Links {
			linkID := LinkEntityID(item.WorkItemID, item.TargetScope)
			existing, err := s.entityStore.GetEntity(ctx, tx, accountID, string(sync.TypeGiteeLink), linkID)
			if err != nil {
				return err
			}

			if existing != nil {
				existingRemote, _ := existing.Fields["remote_id"].(string)
				if existingRemote == item.RemoteID {
					linksUnchanged++
					continue
				} else {
					conflicts = append(conflicts, map[string]any{
						"work_item_id":       item.WorkItemID,
						"target_scope":       item.TargetScope,
						"existing_remote_id": existingRemote,
						"remote_id":          item.RemoteID,
					})
					continue
				}
			}

			pubAt := now.Format(time.RFC3339)
			if item.PublishedAt != nil {
				pubAt = item.PublishedAt.Format(time.RFC3339)
			}

			_, err = s.entityStore.PutSystemEntity(ctx, tx, accountID, &deviceID, string(sync.TypeGiteeLink), linkID, map[string]any{
				"work_item_id":      item.WorkItemID,
				"target_scope":      item.TargetScope,
				"remote_id":         item.RemoteID,
				"url":               item.URL,
				"published_version": 1,
				"published_at":      pubAt,
				"deleted":           false,
			})
			if err != nil {
				return err
			}
			linksUpserted++
		}

		for _, item := range input.Pending {
			reqID := uuid.New()
			_, err := tx.Exec(ctx, `
				INSERT INTO publish_requests (
					id, account_id, work_item_id, work_item_version, target_scope,
					operation, note, state, created_at, updated_at
				)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
				ON CONFLICT (account_id, work_item_id, target_scope) WHERE state IN ('registered', 'sending', 'uncertain') DO NOTHING
			`, reqID, accountID, item.WorkItemID, 1, item.TargetScope, "create", item.Note, StateUncertain, now)
			if err != nil {
				return err
			}
			pendingCreated++
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if conflicts == nil {
		conflicts = []map[string]any{}
	}

	return map[string]any{
		"links_upserted":  linksUpserted,
		"links_unchanged": linksUnchanged,
		"pending_created": pendingCreated,
		"pending_skipped": pendingSkipped,
		"conflicts":       conflicts,
	}, nil
}

func (s *Service) MigrationStatus(ctx context.Context, accountID uuid.UUID) (MigrationStatus, error) {
	var enabled bool
	err := s.pool.QueryRow(ctx, "SELECT unified_publish_enabled FROM accounts WHERE id = $1", accountID).Scan(&enabled)
	if err != nil {
		return MigrationStatus{}, err
	}

	var workItems, links, uncertain int
	_ = s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM sync_entities WHERE account_id = $1 AND entity_type = 'work_item' AND deleted = false", accountID).Scan(&workItems)
	_ = s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM sync_entities WHERE account_id = $1 AND entity_type = 'gitee_link' AND deleted = false", accountID).Scan(&links)
	_ = s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM publish_requests WHERE account_id = $1 AND state = 'uncertain'", accountID).Scan(&uncertain)

	return MigrationStatus{
		UnifiedPublishEnabled: enabled,
		Counts: MigrationCounts{
			WorkItems: workItems,
			Links:     links,
			Uncertain: uncertain,
		},
	}, nil
}

func (s *Service) CompleteMigration(ctx context.Context, accountID uuid.UUID, expected MigrationCounts) (MigrationStatus, error) {
	status, err := s.MigrationStatus(ctx, accountID)
	if err != nil {
		return MigrationStatus{}, err
	}

	if status.Counts.WorkItems != expected.WorkItems ||
		status.Counts.Links != expected.Links ||
		status.Counts.Uncertain != expected.Uncertain {
		return status, httpx.E(http.StatusConflict, "count_mismatch", fmt.Sprintf(
			"migration count mismatch: expected %+v, actual %+v", expected, status.Counts,
		))
	}

	_, err = s.pool.Exec(ctx, "UPDATE accounts SET unified_publish_enabled = true WHERE id = $1", accountID)
	if err != nil {
		return MigrationStatus{}, err
	}

	status.UnifiedPublishEnabled = true
	return status, nil
}

// Sweeper: runs periodically to mark expired registered requests and turn stale sending into uncertain
func (s *Service) Sweep(ctx context.Context) error {
	now := s.nowFn()
	staleThreshold := now.Add(-s.cfg.SendingStaleAfter)

	// Sending -> Uncertain
	_, err := s.pool.Exec(ctx, `
		UPDATE publish_requests
		SET state = 'uncertain',
		    error = 'executor timed out, transitioned to uncertain',
		    updated_at = $1
		WHERE state = 'sending' AND claimed_at < $2
	`, now, staleThreshold)
	if err != nil {
		return fmt.Errorf("sweep stale sending requests: %w", err)
	}

	// Expired registered
	_, err = s.pool.Exec(ctx, `
		UPDATE publish_requests
		SET state = 'expired',
		    updated_at = $1
		WHERE state = 'registered' AND expires_at < $1
	`, now)
	if err != nil {
		return fmt.Errorf("sweep expired registered requests: %w", err)
	}

	return nil
}
