# Work Tracker Server — Protocol Specification (v1)

Status: **binding**. Server, desktop and mobile implementations MUST follow this document.
`api/openapi.yaml` is the machine contract; when the two disagree, fix the discrepancy rather than guessing.

## 1. Accounts, devices and sessions

- An account owns all data. Every row in every business table carries `account_id`; every query filters by the
  account from the authenticated session — never from the request body or path.
- Login creates a **device session**: `POST /api/v1/auth/login` with email, password and a device descriptor
  (`name`, `platform` = `desktop|mobile|web`, `client_version`). Response contains an opaque bearer token.
- Token format: `wts_` + base64url(32 random bytes). Only `sha256(token)` (hex) is stored. Tokens never appear in logs.
- `DELETE /api/v1/devices/{id}` revokes a device; all its sessions stop working immediately.
- `devices.last_seen_at` updates on any authenticated request (at most once per minute);
  `devices.last_sync_at` updates on push, pull and snapshot.

## 2. Entities

Synchronized data is a set of entities identified by `(account_id, entity_type, entity_id)`.

- `entity_id`: client generated, regex `^[A-Za-z0-9_.:-]{1,128}$` (desktop uses uuid hex).
- `version`: int64, `1` on create, `+1` for every accepted change that actually changes a value.
- `field_versions`: map `field -> version at which that field last changed`.
- `fields`: the full current field map. `deleted` (bool) is a regular field present on every entity type.

| entity_type | entity_id | writable by clients | fields |
| --- | --- | --- | --- |
| `work_item` | stable task id | yes | `date` (YYYY-MM-DD, required on create), `title` (1..500, required on create), `details` (≤20000), `project` (≤500, default `未归属项目`), `category` (1..100, default `其他`), `status` (`requested|attempted|changed|verified|delivered|rejected`, default `requested`), `origin` (≤20, default `manual`), `suggested_minutes` (int 0..1440 or null), `confirmed_minutes` (int 0..1440, default 0), `time_locked` (bool, default false), `time_source` (≤40, default `manual`), `time_reason` (≤4000), `started_at`/`ended_at` (RFC3339 with offset or null), `deleted` |
| `daily_report` | the date `YYYY-MM-DD` | yes | `target_minutes` (int 1..43200), `confirmed` (bool), `confirmed_at` (RFC3339 or null), `deleted` |
| `setting` | one of `work_time`, `prompts`, `submit`, `gitee_style`, `manual` (per-account manual-entry templates and project options) | yes | `value` (JSON object, ≤256 KiB serialized), `deleted` |
| `project_mapping` | stable mapping id | yes | `match_mode` (`prefix|contains|exact`), `pattern` (1..500), `target_type` (`enterprise|repository`), `enterprise_id` (int ≥1, required when enterprise), `program_id` (int ≥1, required when enterprise), `issue_type_id` (int ≥1 or null), `repository` (`owner/repo`, required when repository), `deleted` |
| `gitee_link` | `lnk_` + first 32 hex chars of `sha256(work_item_id + "\n" + target_scope)` | **no** (server only) | `work_item_id`, `target_scope`, `remote_id`, `url`, `published_version`, `published_at`, `deleted` |

Unknown fields are rejected (`validation_failed`). Settings are versioned **per category**; mappings **per row**.

Recommended `setting.value` shapes (server only checks "JSON object ≤256 KiB"):
- `work_time`: `{target_minutes, step_minutes, timezone}`
- `prompts`: `{agent_prompts, title_examples, style_template}`
- `submit`: `{enterprise_id, include_details, dedup:{...}, default_target_type, time_format}`
- `gitee_style`: `{title_prefix, description_template, git_commit_template}`

Raw conversations, Git details, local paths, model API keys and Gitee credentials are **never** synchronized.

## 3. Push (`POST /api/v1/sync/push`)

Request: `{ "ops": [ { "op_id", "entity_type", "entity_id", "base_version", "fields": { changed fields only } } ] }`,
1..200 ops, `op_id` is a client UUID.

The whole request runs in **one** PostgreSQL transaction that first locks the account row
(`SELECT change_seq FROM accounts WHERE id=$1 FOR UPDATE`). This serializes writers per account so that
change sequence numbers are allocated in commit order.

For each op, in request order:

1. **Idempotency.** If `(account_id, op_id)` exists in `sync_op_results`, return the stored result unchanged.
2. **Validate** entity type, id, writable-by-client, field names and values. Failure → `rejected` with
   `error.code=validation_failed`. Rejections are stored as results too.
3. **Missing entity.** `base_version=0` → create with defaults + supplied fields (required fields enforced).
   `base_version>0` → `rejected`, `not_found`.
4. **Existing entity — conflict detection.** Let `B = base_version`.
   - `conflict_fields = { f ∈ op.fields : field_versions[f] > B and current[f] ≠ op.fields[f] }`.
   - Delete vs edit: if `op.fields.deleted == true`, `current.deleted == false`, and some other field `g` has
     `field_versions[g] > B` (edited after the client's base), add `deleted` and every such `g` to `conflict_fields`.
   - Edit vs delete: if `current.deleted == true`, `field_versions.deleted > B`, and the op changes a field other than
     setting `deleted=false`, add `deleted` to `conflict_fields`.
   - Non-empty `conflict_fields` → status `conflict`; return the full current entity; **apply nothing** for this op.
5. **Apply.** Fields whose value actually changes get `field_versions[f] = version+1`; `version` increments once.
   If nothing changes: status `applied`, no version bump, no change row.
6. **Change log.** Each applied change increments `accounts.change_seq` and inserts
   `sync_changes(account_id, seq, entity_type, entity_id, version, device_id)`.
7. **Side effect — report invalidation.** Any applied change to a `work_item` sets `confirmed=false`,
   `confirmed_at=null` on the `daily_report` for the item's old and new `date` if that report exists and is
   confirmed (same transaction, its own version bump and change row, attributed to the same device).
8. Store the result in `sync_op_results`.

Response: `{ "results": [ {op_id, status: applied|conflict|rejected, entity?, conflict_fields?, error?} ], "cursor" }`
where `cursor` is the account's `change_seq` after commit. Entities in results always contain the full state.

### Client merge rules (desktop and mobile)

Clients keep a **shadow** = last server-acknowledged state per entity.
- Local dirty fields = fields where local ≠ shadow. Push only dirty fields with `base_version = shadow.version`.
- On `conflict`: three-way merge per field using shadow as base.
  Remote-only change → take remote. Local-only change → keep local. Both changed to different values → record a
  **user-visible conflict** (keep both values; user chooses). Then update shadow to remote and re-push remaining
  local changes with a **new** `op_id` and the new `base_version`.
- Retry after timeout reuses the **same** `op_id` and payload.
- Consecutive local edits coalesce: an unacknowledged op is resent unchanged; changes made after it was created
  become a new op after acknowledgement.

## 4. Pull and snapshot

- `GET /api/v1/sync/pull?cursor=N&limit=500` (limit 1..1000) returns `{changes: [Entity], cursor, has_more}`.
  The server reads `sync_changes` rows with `seq > N` ordered by `seq`, groups to distinct entities, returns their
  **current** state, and sets `cursor` to the largest `seq` read. Clients apply idempotently.
- Changes older than the retention window (`WTS_CHANGE_RETENTION_DAYS`, default 90) are pruned and
  `accounts.min_retained_seq` is raised. If `0 < N < min_retained_seq - 1` → `410` with `error.code=cursor_expired`.
  (`N=0` on an account with pruned history also returns `cursor_expired`; new clients start with a snapshot.)
- `GET /api/v1/sync/snapshot` returns `{entities: [Entity], cursor}` for the whole account from one
  `REPEATABLE READ` transaction; `cursor` is `change_seq` read in that transaction. After a snapshot the client
  replaces its shadows, keeps local pending ops and re-runs the merge rules for them.
- `GET /api/v1/entities/{type}` (optional `from`, `to` for `work_item`/`daily_report` date filter, `include_deleted`)
  and `GET /api/v1/entities/{type}/{id}` are read APIs for online clients (mobile). Writes always go through push.

## 5. Publish coordination

All executors (desktop, mobile, optional cloud worker) share one state machine and one link table.

`target_scope` identifies the Gitee duplicate-detection range: `ent:{enterprise_id}` for enterprise work items,
`repo:{owner}/{repo}` for repository issues. Enterprise links are scoped by **enterprise**, not program, so a
mapping change cannot create a second remote item.

```
registered --claim--> sending --result ok--> succeeded
    |                    |  \--result failed--> failed
    |                    \--result uncertain / stale--> uncertain --reconcile found--> succeeded
    |                                                         \--reconcile not found--> failed
    \--expired (TTL, not claimed) --> expired
    \--cancel--> cancelled
```

In-flight states: `registered`, `sending`, `uncertain`. A partial unique index guarantees at most one in-flight
request per `(account_id, work_item_id, target_scope)`.

1. **Register** `POST /api/v1/publish/batches` with `idempotency_key`, `executor` (`device|cloud`) and items
   `{work_item_id, work_item_version, target_scope, operation (create|update), remote_id?, target (object), payload
   (object ≤64 KiB), config_versions {"setting:submit": 3, "project_mapping:<id>": 2}}` (1..200 items).
   One transaction:
   - Same `idempotency_key` → return the original batch (`200`).
   - `accounts.unified_publish_enabled` must be true → else `409 migration_incomplete`.
   - `executor=cloud` requires `WTS_CLOUD_PUBLISH_ENABLED` → else `403 cloud_publish_disabled`.
   - Expire registered requests of this account whose `expires_at < now()`.
   - Every work item must exist, not be deleted, and have `version == work_item_version`; every config version must
     equal the current entity version → else `409 preview_stale` with per-item details. Nothing is stored.
   - If a `gitee_link` exists for `(work_item_id, target_scope)`: `operation` becomes `update` and `remote_id` is
     taken from the link (`operation_adjusted=true`).
   - Insert requests in state `registered`, `expires_at = now() + WTS_PUBLISH_PREVIEW_TTL` (default 15 min).
     Unique violation → `409 in_flight` listing the blocking request ids. Nothing is stored.
   - Response `201` with the batch and its requests.
2. **Claim** `POST /api/v1/publish/requests/{id}/claim`: atomic
   `UPDATE … SET state='sending', executor_device_id=$dev, claimed_at=now() WHERE id=$1 AND account_id=$2 AND
   state='registered' AND expires_at > now() RETURNING …`. No row → re-read: expired TTL → mark `expired`, `410
   request_expired`; otherwise `409 invalid_state` with current request. Claiming by the same device that already holds
   `sending` returns `200` (idempotent). Gitee is called **outside** any transaction, after a successful claim.
3. **Result** `POST /api/v1/publish/requests/{id}/result` `{outcome: succeeded|failed|uncertain, remote_id?, url?,
   error?}` — only from the claiming device while `sending` (`403 not_executor` / `409 invalid_state` otherwise).
   Repeating the same final outcome returns `200`. `succeeded` requires `remote_id`; it stores `remote_id`, `url`,
   `published_version = work_item_version`, and upserts the `gitee_link` entity (server write: version bump + change
   row, so all devices receive it by pull).
4. **Stale sending.** A sweeper turns `sending` requests with `claimed_at < now() - WTS_SENDING_STALE_AFTER`
   (default 10 min) into `uncertain`.
5. **Reconcile** `POST /api/v1/publish/requests/{id}/reconcile` `{found, remote_id?, url?}` — any device of the
   account, allowed in `uncertain` (or stale `sending`). `found=true` → `succeeded` + link; `found=false` → `failed`.
   Executors verify by remote id or the source marker `<!-- awt-work-item:{marker} -->` before reconciling.
6. **Cancel** `POST /api/v1/publish/requests/{id}/cancel` — only `registered`.
7. **Query** `GET /api/v1/publish/requests?state=&work_item_id=&limit=` and `GET /api/v1/publish/batches/{id}`.

Edits after a claim do not change the request: the result belongs to the confirmed snapshot (`work_item_version`).

## 6. First binding migration

1. Desktop previews counts locally, then pushes all work items, reports, settings and mappings as `base_version=0`
   ops in batches (resumable: same op ids on retry).
2. `POST /api/v1/migration/links` `{links:[{work_item_id, target_scope, remote_id, url, published_at}],
   pending:[{work_item_id, target_scope, note}]}` — upserts links (existing link with a different remote id →
   reported in `conflicts`, not overwritten) and creates `uncertain` publish requests for pending/uncertain local
   records (skipped if one is already in flight). Idempotent.
3. `GET /api/v1/migration/status` returns `{unified_publish_enabled, counts:{work_items, links, uncertain}}`.
4. `POST /api/v1/migration/complete` `{expected:{work_items, links, uncertain}}` compares with server counts
   (non-deleted work items, non-deleted links, uncertain requests). Equal → `unified_publish_enabled=true`;
   otherwise `409 count_mismatch` with actual counts.

## 7. Errors

All errors: `{"error": {"code": "snake_case", "message": "human readable", "details": {...optional}}}`.

| HTTP | codes |
| --- | --- |
| 400 | `validation_failed`, `bad_request` |
| 401 | `unauthorized` |
| 403 | `forbidden`, `not_executor`, `cloud_publish_disabled`, `registration_disabled` |
| 404 | `not_found` |
| 409 | `conflict`, `email_taken`, `preview_stale`, `in_flight`, `invalid_state`, `migration_incomplete`, `count_mismatch` |
| 410 | `cursor_expired`, `request_expired` |
| 413 | `payload_too_large` |
| 429 | `rate_limited` |
| 500 | `internal` (no internal detail leaked) |

Resources of another account are reported as `404 not_found`, never `403`.
