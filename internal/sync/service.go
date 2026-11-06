package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yiwili/work-tracker-server/internal/db"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

type Service struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:  pool,
		nowFn: time.Now,
	}
}

func (s *Service) SetNow(fn func() time.Time) {
	s.nowFn = fn
}

// LockAccount locks the account row FOR UPDATE to serialize writers per account
func (s *Service) LockAccount(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error {
	var seq int64
	err := tx.QueryRow(ctx, "SELECT change_seq FROM accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.E(http.StatusNotFound, "not_found", "account not found")
		}
		return fmt.Errorf("lock account: %w", err)
	}
	return nil
}

type QueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Service) GetEntity(ctx context.Context, q QueryRower, accountID uuid.UUID, entityType string, entityID string) (*Entity, error) {
	var row = q.QueryRow(ctx, `
		SELECT entity_type, entity_id, version, field_versions, fields, updated_at, updated_by_device
		FROM sync_entities
		WHERE account_id = $1 AND entity_type = $2 AND entity_id = $3
	`, accountID, entityType, entityID)

	var e Entity
	var fvJSON, fJSON []byte

	err := row.Scan(&e.EntityType, &e.EntityID, &e.Version, &fvJSON, &fJSON, &e.UpdatedAt, &e.UpdatedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan entity: %w", err)
	}

	if err := json.Unmarshal(fvJSON, &e.FieldVersions); err != nil {
		return nil, fmt.Errorf("unmarshal field_versions: %w", err)
	}
	if err := json.Unmarshal(fJSON, &e.Fields); err != nil {
		return nil, fmt.Errorf("unmarshal fields: %w", err)
	}

	return &e, nil
}

func (s *Service) PutSystemEntity(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, deviceID *uuid.UUID, entityType, entityID string, fields map[string]any) (*Entity, error) {
	now := s.nowFn()
	current, err := s.GetEntity(ctx, tx, accountID, entityType, entityID)
	if err != nil {
		return nil, err
	}

	var version int64 = 1
	fieldVersions := make(map[string]int64)
	currentFields := make(map[string]any)

	if current != nil {
		version = current.Version + 1
		fieldVersions = current.FieldVersions
		currentFields = current.Fields
	}

	for k, v := range fields {
		if current == nil || !reflect.DeepEqual(currentFields[k], v) {
			fieldVersions[k] = version
			currentFields[k] = v
		}
	}

	deleted, _ := currentFields["deleted"].(bool)
	fvJSON, _ := json.Marshal(fieldVersions)
	fJSON, _ := json.Marshal(currentFields)

	_, err = tx.Exec(ctx, `
		INSERT INTO sync_entities (account_id, entity_type, entity_id, version, field_versions, fields, deleted, updated_at, updated_by_device)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (account_id, entity_type, entity_id) DO UPDATE
		SET version = $4,
		    field_versions = $5,
		    fields = $6,
		    deleted = $7,
		    updated_at = $8,
		    updated_by_device = $9
	`, accountID, entityType, entityID, version, fvJSON, fJSON, deleted, now, deviceID)
	if err != nil {
		return nil, fmt.Errorf("upsert system entity: %w", err)
	}

	// Increment account change_seq and write sync_changes
	var newSeq int64
	err = tx.QueryRow(ctx, "UPDATE accounts SET change_seq = change_seq + 1 WHERE id=$1 RETURNING change_seq", accountID).Scan(&newSeq)
	if err != nil {
		return nil, fmt.Errorf("increment change_seq: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO sync_changes (account_id, seq, entity_type, entity_id, version, device_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, accountID, newSeq, entityType, entityID, version, deviceID, now)
	if err != nil {
		return nil, fmt.Errorf("insert sync_changes: %w", err)
	}

	return &Entity{
		EntityType:    EntityType(entityType),
		EntityID:      entityID,
		Version:       version,
		FieldVersions: fieldVersions,
		Fields:        currentFields,
		UpdatedAt:     now,
		UpdatedBy:     deviceID,
	}, nil
}

func (s *Service) Push(ctx context.Context, accountID, deviceID uuid.UUID, ops []SyncOp) (PushResponse, error) {
	if len(ops) == 0 || len(ops) > 200 {
		return PushResponse{}, httpx.E(http.StatusBadRequest, "validation_failed", "ops must contain between 1 and 200 operations")
	}

	var results []SyncOpResult
	var finalCursor int64

	err := db.InTx(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := s.LockAccount(ctx, tx, accountID); err != nil {
			return err
		}

		now := s.nowFn()

		for _, op := range ops {
			// 1. Idempotency check
			var cachedResultJSON []byte
			err := tx.QueryRow(ctx, "SELECT result FROM sync_op_results WHERE account_id=$1 AND op_id=$2", accountID, op.OpID).Scan(&cachedResultJSON)
			if err == nil {
				var cached SyncOpResult
				if err := json.Unmarshal(cachedResultJSON, &cached); err == nil {
					results = append(results, cached)
					continue
				}
			}

			// 2. Validate
			if !IsValidEntityType(string(op.EntityType)) || !IsClientWritable(op.EntityType) {
				res := SyncOpResult{
					OpID:   op.OpID,
					Status: "rejected",
					Error:  &OpError{Code: "validation_failed", Message: fmt.Sprintf("invalid or non-writable entity_type: %s", op.EntityType)},
				}
				s.storeOpResult(ctx, tx, accountID, op.OpID, res)
				results = append(results, res)
				continue
			}

			if err := ValidateEntityID(op.EntityID); err != nil {
				res := SyncOpResult{
					OpID:   op.OpID,
					Status: "rejected",
					Error:  &OpError{Code: "validation_failed", Message: err.Error()},
				}
				s.storeOpResult(ctx, tx, accountID, op.OpID, res)
				results = append(results, res)
				continue
			}

			isCreate := (op.BaseVersion == 0)
			if err := ValidateFields(op.EntityType, op.Fields, isCreate); err != nil {
				res := SyncOpResult{
					OpID:   op.OpID,
					Status: "rejected",
					Error:  &OpError{Code: "validation_failed", Message: err.Error()},
				}
				s.storeOpResult(ctx, tx, accountID, op.OpID, res)
				results = append(results, res)
				continue
			}

			if op.EntityType == TypeGiteeLink {
				wiID, _ := op.Fields["work_item_id"].(string)
				if wiID != "" {
					wi, err := s.GetEntity(ctx, tx, accountID, string(TypeWorkItem), wiID)
					if err != nil {
						return err
					}
					if wi == nil {
						res := SyncOpResult{
							OpID:   op.OpID,
							Status: "rejected",
							Error:  &OpError{Code: "work_item_not_found", Message: fmt.Sprintf("referenced work item '%s' not found", wiID)},
						}
						s.storeOpResult(ctx, tx, accountID, op.OpID, res)
						results = append(results, res)
						continue
					}
				}
			}

			// 3. Check existing entity
			current, err := s.GetEntity(ctx, tx, accountID, string(op.EntityType), op.EntityID)
			if err != nil {
				return err
			}

			if current == nil {
				if op.BaseVersion > 0 {
					res := SyncOpResult{
						OpID:   op.OpID,
						Status: "rejected",
						Error:  &OpError{Code: "not_found", Message: "entity does not exist but base_version > 0"},
					}
					s.storeOpResult(ctx, tx, accountID, op.OpID, res)
					results = append(results, res)
					continue
				}

				// Create entity
				fields := make(map[string]any)
				FillDefaults(op.EntityType, fields)
				for k, v := range op.Fields {
					fields[k] = v
				}

				var version int64 = 1
				fieldVersions := make(map[string]int64)
				for k := range fields {
					fieldVersions[k] = version
				}

				deleted, _ := fields["deleted"].(bool)
				fvJSON, _ := json.Marshal(fieldVersions)
				fJSON, _ := json.Marshal(fields)

				_, err = tx.Exec(ctx, `
					INSERT INTO sync_entities (account_id, entity_type, entity_id, version, field_versions, fields, deleted, updated_at, updated_by_device)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				`, accountID, op.EntityType, op.EntityID, version, fvJSON, fJSON, deleted, now, deviceID)
				if err != nil {
					return fmt.Errorf("insert new entity: %w", err)
				}

				seq, err := s.recordChange(ctx, tx, accountID, &deviceID, string(op.EntityType), op.EntityID, version, now)
				if err != nil {
					return err
				}
				finalCursor = seq

				newEnt := &Entity{
					EntityType:    op.EntityType,
					EntityID:      op.EntityID,
					Version:       version,
					FieldVersions: fieldVersions,
					Fields:        fields,
					UpdatedAt:     now,
					UpdatedBy:     &deviceID,
				}

				s.invalidateReportIfNeeded(ctx, tx, accountID, &deviceID, op.EntityType, fields, nil, now)

				res := SyncOpResult{
					OpID:   op.OpID,
					Status: "applied",
					Entity: newEnt,
				}
				s.storeOpResult(ctx, tx, accountID, op.OpID, res)
				results = append(results, res)
				continue
			}

			// 4. Conflict detection
			B := op.BaseVersion
			conflictSet := make(map[string]bool)

			for f, opVal := range op.Fields {
				lastChangedVer := current.FieldVersions[f]
				if lastChangedVer > B && !reflect.DeepEqual(current.Fields[f], opVal) {
					conflictSet[f] = true
				}
			}

			// Delete vs edit
			opDeleted, opHasDeleted := op.Fields["deleted"].(bool)
			curDeleted, _ := current.Fields["deleted"].(bool)

			if opHasDeleted && opDeleted && !curDeleted {
				for g, v := range current.FieldVersions {
					if v > B && g != "deleted" {
						conflictSet["deleted"] = true
						conflictSet[g] = true
					}
				}
			}

			// Edit vs delete
			if curDeleted && current.FieldVersions["deleted"] > B {
				if !(opHasDeleted && !opDeleted) {
					conflictSet["deleted"] = true
				}
			}

			if len(conflictSet) > 0 {
				var conflictFields []string
				for k := range conflictSet {
					conflictFields = append(conflictFields, k)
				}
				sort.Strings(conflictFields)

				res := SyncOpResult{
					OpID:           op.OpID,
					Status:         "conflict",
					Entity:         current,
					ConflictFields: conflictFields,
				}
				s.storeOpResult(ctx, tx, accountID, op.OpID, res)
				results = append(results, res)
				continue
			}

			// 5. Apply changes
			hasActualChange := false
			newFields := make(map[string]any)
			for k, v := range current.Fields {
				newFields[k] = v
			}
			newFieldVersions := make(map[string]int64)
			for k, v := range current.FieldVersions {
				newFieldVersions[k] = v
			}

			nextVersion := current.Version + 1

			for k, v := range op.Fields {
				if !reflect.DeepEqual(current.Fields[k], v) {
					hasActualChange = true
					newFields[k] = v
					newFieldVersions[k] = nextVersion
				}
			}

			if !hasActualChange {
				res := SyncOpResult{
					OpID:   op.OpID,
					Status: "applied",
					Entity: current,
				}
				s.storeOpResult(ctx, tx, accountID, op.OpID, res)
				results = append(results, res)
				continue
			}

			deleted, _ := newFields["deleted"].(bool)
			fvJSON, _ := json.Marshal(newFieldVersions)
			fJSON, _ := json.Marshal(newFields)

			_, err = tx.Exec(ctx, `
				UPDATE sync_entities
				SET version = $1,
				    field_versions = $2,
				    fields = $3,
				    deleted = $4,
				    updated_at = $5,
				    updated_by_device = $6
				WHERE account_id = $7 AND entity_type = $8 AND entity_id = $9
			`, nextVersion, fvJSON, fJSON, deleted, now, deviceID, accountID, op.EntityType, op.EntityID)
			if err != nil {
				return fmt.Errorf("update entity: %w", err)
			}

			seq, err := s.recordChange(ctx, tx, accountID, &deviceID, string(op.EntityType), op.EntityID, nextVersion, now)
			if err != nil {
				return err
			}
			finalCursor = seq

			appliedEnt := &Entity{
				EntityType:    op.EntityType,
				EntityID:      op.EntityID,
				Version:       nextVersion,
				FieldVersions: newFieldVersions,
				Fields:        newFields,
				UpdatedAt:     now,
				UpdatedBy:     &deviceID,
			}

			s.invalidateReportIfNeeded(ctx, tx, accountID, &deviceID, op.EntityType, newFields, current.Fields, now)

			res := SyncOpResult{
				OpID:   op.OpID,
				Status: "applied",
				Entity: appliedEnt,
			}
			s.storeOpResult(ctx, tx, accountID, op.OpID, res)
			results = append(results, res)
		}

		if finalCursor == 0 {
			_ = tx.QueryRow(ctx, "SELECT change_seq FROM accounts WHERE id=$1", accountID).Scan(&finalCursor)
		}

		return nil
	})

	if err != nil {
		return PushResponse{}, err
	}

	return PushResponse{
		Results: results,
		Cursor:  finalCursor,
	}, nil
}

func (s *Service) recordChange(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, deviceID *uuid.UUID, entityType, entityID string, version int64, now time.Time) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, "UPDATE accounts SET change_seq = change_seq + 1 WHERE id=$1 RETURNING change_seq", accountID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("increment change_seq: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO sync_changes (account_id, seq, entity_type, entity_id, version, device_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, accountID, seq, entityType, entityID, version, deviceID, now)
	if err != nil {
		return 0, fmt.Errorf("insert sync_changes: %w", err)
	}

	return seq, nil
}

func (s *Service) storeOpResult(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, opID uuid.UUID, res SyncOpResult) {
	bytes, _ := json.Marshal(res)
	_, _ = tx.Exec(ctx, `
		INSERT INTO sync_op_results (account_id, op_id, result)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id, op_id) DO UPDATE SET result = $3
	`, accountID, opID, bytes)
}

func (s *Service) invalidateReportIfNeeded(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, deviceID *uuid.UUID, entityType EntityType, newFields, oldFields map[string]any, now time.Time) {
	if entityType != TypeWorkItem {
		return
	}

	dates := make(map[string]bool)
	if d, ok := newFields["date"].(string); ok && d != "" {
		dates[d] = true
	}
	if oldFields != nil {
		if d, ok := oldFields["date"].(string); ok && d != "" {
			dates[d] = true
		}
	}

	for date := range dates {
		rep, err := s.GetEntity(ctx, tx, accountID, string(TypeDailyReport), date)
		if err != nil || rep == nil {
			continue
		}
		confirmed, _ := rep.Fields["confirmed"].(bool)
		if confirmed {
			// Invalidate report
			_, _ = s.PutSystemEntity(ctx, tx, accountID, deviceID, string(TypeDailyReport), date, map[string]any{
				"confirmed":    false,
				"confirmed_at": nil,
			})
		}
	}
}

func (s *Service) Pull(ctx context.Context, accountID uuid.UUID, cursor int64, limit int) (PullResponse, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}

	var minRetainedSeq, changeSeq int64
	err := s.pool.QueryRow(ctx, "SELECT min_retained_seq, change_seq FROM accounts WHERE id=$1", accountID).Scan(&minRetainedSeq, &changeSeq)
	if err != nil {
		return PullResponse{}, fmt.Errorf("query account: %w", err)
	}

	if cursor > 0 && cursor < minRetainedSeq-1 {
		return PullResponse{}, httpx.E(http.StatusGone, "cursor_expired", "sync cursor has expired, snapshot recovery required")
	}

	// Query distinct entities changed after cursor
	rows, err := s.pool.Query(ctx, `
		SELECT entity_type, entity_id, MAX(seq) as max_seq
		FROM sync_changes
		WHERE account_id = $1 AND seq > $2
		GROUP BY entity_type, entity_id
		ORDER BY max_seq ASC
		LIMIT $3
	`, accountID, cursor, limit+1)
	if err != nil {
		return PullResponse{}, fmt.Errorf("query sync changes: %w", err)
	}
	defer rows.Close()

	type changeRef struct {
		entityType string
		entityID   string
		seq        int64
	}
	var refs []changeRef
	for rows.Next() {
		var ref changeRef
		if err := rows.Scan(&ref.entityType, &ref.entityID, &ref.seq); err != nil {
			return PullResponse{}, err
		}
		refs = append(refs, ref)
	}

	hasMore := len(refs) > limit
	if hasMore {
		refs = refs[:limit]
	}

	var entities []Entity
	var maxCursor = cursor

	for _, ref := range refs {
		ent, err := s.GetEntity(ctx, s.pool, accountID, ref.entityType, ref.entityID)
		if err != nil {
			return PullResponse{}, fmt.Errorf("read pulled entity (%s, %s): %w", ref.entityType, ref.entityID, err)
		}
		if ent == nil {
			continue
		}
		if ref.seq > maxCursor {
			maxCursor = ref.seq
		}
		entities = append(entities, *ent)
	}

	if len(refs) == 0 {
		maxCursor = changeSeq
	}

	if entities == nil {
		entities = []Entity{}
	}

	return PullResponse{
		Changes: entities,
		Cursor:  maxCursor,
		HasMore: hasMore,
	}, nil
}

func (s *Service) Snapshot(ctx context.Context, accountID uuid.UUID) (SnapshotResponse, error) {
	var cursor int64
	var entities []Entity

	err := db.InTx(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT change_seq FROM accounts WHERE id=$1", accountID).Scan(&cursor)
		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT entity_type, entity_id, version, field_versions, fields, updated_at, updated_by_device
			FROM sync_entities
			WHERE account_id = $1
			ORDER BY entity_type, entity_id
		`, accountID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var e Entity
			var fvJSON, fJSON []byte
			if err := rows.Scan(&e.EntityType, &e.EntityID, &e.Version, &fvJSON, &fJSON, &e.UpdatedAt, &e.UpdatedBy); err != nil {
				return err
			}
			_ = json.Unmarshal(fvJSON, &e.FieldVersions)
			_ = json.Unmarshal(fJSON, &e.Fields)
			entities = append(entities, e)
		}

		return nil
	})

	if err != nil {
		return SnapshotResponse{}, err
	}

	if entities == nil {
		entities = []Entity{}
	}

	return SnapshotResponse{
		Entities: entities,
		Cursor:   cursor,
	}, nil
}

func (s *Service) ListEntities(ctx context.Context, accountID uuid.UUID, entityType string, from, to string, includeDeleted bool) ([]Entity, int64, error) {
	var cursor int64
	err := s.pool.QueryRow(ctx, "SELECT change_seq FROM accounts WHERE id=$1", accountID).Scan(&cursor)
	if err != nil {
		return nil, 0, err
	}

	query := `
		SELECT entity_type, entity_id, version, field_versions, fields, updated_at, updated_by_device
		FROM sync_entities
		WHERE account_id = $1 AND entity_type = $2
	`
	args := []any{accountID, entityType}

	if !includeDeleted {
		query += " AND deleted = false"
	}

	if from != "" && (entityType == string(TypeWorkItem) || entityType == string(TypeDailyReport)) {
		if entityType == string(TypeWorkItem) {
			query += fmt.Sprintf(" AND fields->>'date' >= $%d", len(args)+1)
		} else {
			query += fmt.Sprintf(" AND entity_id >= $%d", len(args)+1)
		}
		args = append(args, from)
	}

	if to != "" && (entityType == string(TypeWorkItem) || entityType == string(TypeDailyReport)) {
		if entityType == string(TypeWorkItem) {
			query += fmt.Sprintf(" AND fields->>'date' <= $%d", len(args)+1)
		} else {
			query += fmt.Sprintf(" AND entity_id <= $%d", len(args)+1)
		}
		args = append(args, to)
	}

	query += " ORDER BY entity_id"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		var fvJSON, fJSON []byte
		if err := rows.Scan(&e.EntityType, &e.EntityID, &e.Version, &fvJSON, &fJSON, &e.UpdatedAt, &e.UpdatedBy); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(fvJSON, &e.FieldVersions)
		_ = json.Unmarshal(fJSON, &e.Fields)
		entities = append(entities, e)
	}

	if entities == nil {
		entities = []Entity{}
	}

	return entities, cursor, nil
}
