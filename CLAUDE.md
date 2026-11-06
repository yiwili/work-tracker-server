# CLAUDE.md — work-tracker-server engineering rules

You are implementing part of a Go service under a lead engineer. Follow these rules exactly. Do not redesign.
If something in the spec is impossible, stop and write the question into your final report instead of improvising.

## Read first
1. `docs/protocol.md` — binding behaviour (sync, conflicts, publish state machine, migration, errors).
2. `api/openapi.yaml` — binding HTTP contract (paths, JSON field names, status codes).
3. `migrations/0001_init.up.sql` — binding schema. Never edit an applied migration; add `000N_name.up.sql`.

## Stack (fixed)
- Go 1.27, module `github.com/yiwili/work-tracker-server`.
- HTTP: `github.com/gin-gonic/gin`. DB: `github.com/jackc/pgx/v5` (`pgxpool`) — **no ORM**, plain SQL.
- Passwords: `golang.org/x/crypto/bcrypt` (cost 12; tests may use `bcrypt.MinCost` via config).
- IDs: `github.com/google/uuid`. Logging: standard `log/slog` JSON handler.
- Tests: standard `testing` + `github.com/stretchr/testify/require`. No other dependencies without a reason in the report.
- If `go mod download` is slow, use `GOPROXY=https://goproxy.cn,direct`.

## Layout (fixed)
```
cmd/server/main.go          # wiring only: config -> db -> migrate -> modules -> router -> http.Server, graceful shutdown
internal/config             # env config (WTS_* variables), defaults, validation
internal/db                 # pgxpool setup, embedded migration runner (advisory lock), InTx helper
internal/httpx              # error envelope, JSON binding, auth middleware, request id, access log, context helpers
internal/ratelimit          # Limiter interface + in-memory token bucket; Redis adapter is an interface stub only
internal/auth               # accounts, devices, sessions: service + handlers
internal/sync               # entity validation, push/pull/snapshot/entities, report invalidation
internal/publish            # batches, requests, claim/result/reconcile/cancel, links, migration endpoints, sweeper
internal/jobs               # PostgreSQL job queue (FOR UPDATE SKIP LOCKED), outbox table, worker loop
internal/server             # gin router: mounts module handlers under /api/v1, health endpoints
internal/testutil           # throwaway schema per test against TEST_DATABASE_URL
migrations/                 # *.up.sql + embed.go (package migrations, //go:embed *.sql)
api/openapi.yaml            # contract
deploy/                     # docker compose, Caddy, tier profiles
```
Each module exposes `type Service struct` (business logic, takes `*pgxpool.Pool` and config) and
`func (h *Handler) Register(rg *gin.RouterGroup)`. Handlers are thin: bind → call service → write JSON.

## Configuration (env)
| var | default | meaning |
| --- | --- | --- |
| `WTS_HTTP_ADDR` | `:8080` | listen address |
| `WTS_DATABASE_URL` | (required) | PostgreSQL DSN |
| `WTS_DB_MAX_CONNS` | `4` | pgxpool max conns (tier: 4 / 8 / 12) |
| `WTS_ALLOW_REGISTRATION` | `true` | self registration |
| `WTS_CLOUD_PUBLISH_ENABLED` | `false` | cloud executor feature flag |
| `WTS_CHANGE_RETENTION_DAYS` | `90` | sync change log retention |
| `WTS_PUBLISH_PREVIEW_TTL` | `15m` | registered request TTL |
| `WTS_SENDING_STALE_AFTER` | `10m` | sending -> uncertain |
| `WTS_SWEEP_INTERVAL` | `1m` | maintenance loop |
| `WTS_BCRYPT_COST` | `12` | bcrypt cost |
| `WTS_LOG_LEVEL` | `info` | slog level |
| `WTS_TRUSTED_PROXIES` | `` | comma separated CIDRs for gin |
| `WTS_MAX_BODY_BYTES` | `4194304` | request body cap → 413 |

## Coding rules
- Every SQL statement touching business tables includes `account_id = $n` from `httpx.AccountID(c)`.
  Cross-account access returns `404 not_found`.
- Errors: return `*httpx.Error{Status, Code, Message, Details}` from services; handlers call `httpx.Fail(c, err)`.
  Unknown errors → 500 `internal`, logged with request id, message never leaked.
- Transactions: `db.InTx(ctx, pool, pgx.TxOptions{...}, func(tx pgx.Tx) error)`. No network calls inside a tx.
- Time: store `timestamptz`, emit RFC3339 UTC in JSON. Use an injectable `now func() time.Time` in services.
- JSON field names: snake_case exactly as in OpenAPI. Use explicit response structs, never `map[string]any` for
  top-level responses (fields maps inside entities are fine).
- Never log tokens, passwords, `payload`, `fields` contents or work item text. Log ids, states, durations.
- Keep functions small; comments explain *why*. No dead code, no TODO stubs except the documented Redis adapter.
- gofmt + `go vet ./...` must be clean.

## Tests (required for every module)
- Integration tests use `testutil.NewDB(t)` which: reads `TEST_DATABASE_URL` (skip test if unset), creates schema
  `t_<random>`, sets `search_path` on a dedicated pool, runs migrations, and drops the schema on cleanup.
- HTTP tests go through the real gin router (`server.New(...)`) with `httptest`.
- Run: `go test ./...` with `TEST_DATABASE_URL=postgres://postgres:awt_local_pg@localhost:5432/postgres?sslmode=disable`.

## Shared contracts between modules (do not change signatures)
`internal/httpx`:
```go
type Error struct { Status int; Code, Message string; Details any }
func (e *Error) Error() string
func E(status int, code, msg string) *Error
func Fail(c *gin.Context, err error)
func BindJSON(c *gin.Context, dst any) error          // DisallowUnknownFields; returns *Error 400 validation_failed
func AccountID(c *gin.Context) uuid.UUID               // set by auth middleware
func DeviceID(c *gin.Context) uuid.UUID
```
`internal/auth`:
```go
func (s *Service) Middleware() gin.HandlerFunc          // Bearer auth, sets account/device in context, 401 otherwise
```
`internal/publish` consumes sync through a consumer-side interface (defined in package publish):
```go
type EntityStore interface {
    // Locks the account row (FOR UPDATE) — call first in any tx that writes entities.
    LockAccount(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error
    // Returns current entity or (nil, nil) if absent.
    GetEntity(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, entityType, entityID string) (*sync.Entity, error)
    // Server-side write (used for gitee_link). Merges fields, bumps version, appends change row. Caller holds LockAccount.
    PutSystemEntity(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, deviceID *uuid.UUID, entityType, entityID string, fields map[string]any) (*sync.Entity, error)
}
```
`*sync.Service` MUST satisfy `publish.EntityStore`. `sync.Entity`:
```go
type Entity struct {
    EntityType    string           `json:"entity_type"`
    EntityID      string           `json:"entity_id"`
    Version       int64            `json:"version"`
    FieldVersions map[string]int64 `json:"field_versions"`
    Fields        map[string]any   `json:"fields"`
    UpdatedAt     time.Time        `json:"updated_at"`
    UpdatedBy     *uuid.UUID       `json:"updated_by_device"`
}
```

## Git
- Conventional Commits (`feat: …`, `fix: …`, `test: …`, `chore: …`). One logical feature per commit.
- Only commit files in your assigned ownership area. Never push. Never rewrite history.
