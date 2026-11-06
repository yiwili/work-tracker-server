-- 0002_constraints: account composite foreign keys, gitee_links table, and sync_entities data constraints.

-- 1. Composite unique constraint on devices(account_id, id) and sessions composite FK
ALTER TABLE devices ADD CONSTRAINT devices_account_id_id_uq UNIQUE (account_id, id);

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_device_id_fkey;
ALTER TABLE sessions ADD CONSTRAINT sessions_account_device_fk
    FOREIGN KEY (account_id, device_id) REFERENCES devices(account_id, id) ON DELETE CASCADE;

-- 2. Composite unique constraint on publish_batches(account_id, id) and publish_requests composite FK
ALTER TABLE publish_batches ADD CONSTRAINT publish_batches_account_id_id_uq UNIQUE (account_id, id);

ALTER TABLE publish_requests DROP CONSTRAINT IF EXISTS publish_requests_batch_id_fkey;
ALTER TABLE publish_requests ADD CONSTRAINT publish_requests_account_batch_fk
    FOREIGN KEY (account_id, batch_id) REFERENCES publish_batches(account_id, id) ON DELETE CASCADE;

-- 3. Dedicated gitee_links table for relational lookups and constraints
CREATE TABLE IF NOT EXISTS gitee_links (
    account_id       uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    work_item_id     text NOT NULL,
    target_scope     text NOT NULL,
    remote_id        text NOT NULL,
    url              text NOT NULL DEFAULT '',
    publish_req_id   uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, work_item_id, target_scope)
);
CREATE INDEX IF NOT EXISTS gitee_links_remote_idx ON gitee_links (account_id, target_scope, remote_id);

-- 4. Check constraints on sync_entities for work_item validity
ALTER TABLE sync_entities ADD CONSTRAINT sync_entities_work_item_date_chk
    CHECK (entity_type != 'work_item' OR (fields->>'date' ~ '^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$'));

ALTER TABLE sync_entities ADD CONSTRAINT sync_entities_work_item_title_chk
    CHECK (entity_type != 'work_item' OR (fields ? 'title' AND length(trim(both from fields->>'title')) > 0));

ALTER TABLE sync_entities ADD CONSTRAINT sync_entities_work_item_minutes_chk
    CHECK (entity_type != 'work_item' OR (
        (NOT (fields ? 'confirmed_minutes') OR fields->>'confirmed_minutes' IS NULL OR fields->>'confirmed_minutes' ~ '^(0|[1-9][0-9]{0,2}|1[0-3][0-9]{2}|14[0-3][0-9]|1440)$') AND
        (NOT (fields ? 'suggested_minutes') OR fields->>'suggested_minutes' IS NULL OR fields->>'suggested_minutes' ~ '^(0|[1-9][0-9]{0,2}|1[0-3][0-9]{2}|14[0-3][0-9]|1440)$')
    ));
