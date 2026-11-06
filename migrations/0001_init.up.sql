-- 0001_init: accounts, devices, sync entity store, publish coordination, jobs.
-- Binding schema; see docs/protocol.md. Never edit an applied migration — add a new file.

CREATE TABLE accounts (
    id                      uuid PRIMARY KEY,
    email                   text NOT NULL,
    password_hash           text NOT NULL,
    display_name            text NOT NULL DEFAULT '',
    change_seq              bigint NOT NULL DEFAULT 0,
    min_retained_seq        bigint NOT NULL DEFAULT 0,
    unified_publish_enabled boolean NOT NULL DEFAULT false,
    cloud_publish_authorized boolean NOT NULL DEFAULT false,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX accounts_email_lower_uq ON accounts (lower(email));

CREATE TABLE devices (
    id             uuid PRIMARY KEY,
    account_id     uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name           text NOT NULL,
    platform       text NOT NULL CHECK (platform IN ('desktop', 'mobile', 'web')),
    client_version text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz,
    last_sync_at   timestamptz,
    revoked_at     timestamptz
);
CREATE INDEX devices_account_idx ON devices (account_id);

CREATE TABLE sessions (
    token_hash   text PRIMARY KEY,               -- hex sha256 of the bearer token
    account_id   uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id    uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);
CREATE INDEX sessions_device_idx ON sessions (device_id);

-- Generic synchronized entity store (work_item, daily_report, setting, project_mapping, gitee_link).
CREATE TABLE sync_entities (
    account_id      uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    entity_type     text NOT NULL,
    entity_id       text NOT NULL,
    version         bigint NOT NULL,
    field_versions  jsonb NOT NULL,
    fields          jsonb NOT NULL,
    deleted         boolean NOT NULL DEFAULT false,   -- mirror of fields->'deleted' for indexing
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by_device uuid,
    PRIMARY KEY (account_id, entity_type, entity_id)
);
CREATE INDEX sync_entities_date_idx ON sync_entities (account_id, entity_type, (fields->>'date'))
    WHERE entity_type IN ('work_item');

CREATE TABLE sync_changes (
    account_id  uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    entity_type text NOT NULL,
    entity_id   text NOT NULL,
    version     bigint NOT NULL,
    device_id   uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, seq)
);
CREATE INDEX sync_changes_created_idx ON sync_changes (created_at);

CREATE TABLE sync_op_results (
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    op_id      uuid NOT NULL,
    result     jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, op_id)
);
CREATE INDEX sync_op_results_created_idx ON sync_op_results (created_at);

-- Publish coordination.
CREATE TABLE publish_batches (
    id               uuid PRIMARY KEY,
    account_id       uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    idempotency_key  text NOT NULL,
    executor         text NOT NULL CHECK (executor IN ('device', 'cloud')),
    created_by_device uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, idempotency_key)
);

CREATE TABLE publish_requests (
    id                 uuid PRIMARY KEY,
    account_id         uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    batch_id           uuid REFERENCES publish_batches(id) ON DELETE CASCADE,  -- null for imported uncertain records
    work_item_id       text NOT NULL,
    work_item_version  bigint NOT NULL,
    target_scope       text NOT NULL,
    operation          text NOT NULL CHECK (operation IN ('create', 'update')),
    operation_adjusted boolean NOT NULL DEFAULT false,
    remote_id          text NOT NULL DEFAULT '',
    url                text NOT NULL DEFAULT '',
    target             jsonb NOT NULL DEFAULT '{}'::jsonb,
    payload            jsonb NOT NULL DEFAULT '{}'::jsonb,
    config_versions    jsonb NOT NULL DEFAULT '{}'::jsonb,
    executor           text NOT NULL DEFAULT 'device' CHECK (executor IN ('device', 'cloud')),
    executor_device_id uuid,
    state              text NOT NULL CHECK (state IN ('registered', 'sending', 'succeeded', 'failed', 'uncertain', 'expired', 'cancelled')),
    error              text NOT NULL DEFAULT '',
    note               text NOT NULL DEFAULT '',
    published_version  bigint,
    expires_at         timestamptz,
    claimed_at         timestamptz,
    finished_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX publish_requests_in_flight_uq ON publish_requests (account_id, work_item_id, target_scope)
    WHERE state IN ('registered', 'sending', 'uncertain');
CREATE INDEX publish_requests_state_idx ON publish_requests (account_id, state);
CREATE INDEX publish_requests_batch_idx ON publish_requests (batch_id);

-- Background jobs (PostgreSQL queue; JetStream mode only transports job ids).
CREATE TABLE jobs (
    id           bigserial PRIMARY KEY,
    account_id   uuid REFERENCES accounts(id) ON DELETE CASCADE,
    kind         text NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    state        text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    attempts     int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL DEFAULT 5,
    run_after    timestamptz NOT NULL DEFAULT now(),
    locked_by    text NOT NULL DEFAULT '',
    locked_until timestamptz,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_ready_idx ON jobs (state, run_after) WHERE state IN ('queued', 'running');

-- Transactional outbox for the optional JetStream transport.
CREATE TABLE job_outbox (
    id         bigserial PRIMARY KEY,
    job_id     bigint NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    subject    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);
CREATE INDEX job_outbox_unsent_idx ON job_outbox (id) WHERE sent_at IS NULL;
