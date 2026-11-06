-- 0003_history_work_items.up.sql: Dedicated history work items table isolated by account.

CREATE TABLE IF NOT EXISTS history_work_items (
    account_id       uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    id               varchar(64) NOT NULL,          -- Desktop SHA-256 stable hex ID
    title            text NOT NULL,                 -- Redacted title (<=500 chars)
    details          text NOT NULL DEFAULT '',      -- Redacted details (<=20000 chars)
    project          text NOT NULL DEFAULT '',      -- Project name or path (<=500 chars)
    work_date        date,                          -- Work date, NULL when missing
    source           varchar(32) NOT NULL DEFAULT 'file', -- 'file', 'gitee', 'git', etc.
    source_id        varchar(200) NOT NULL DEFAULT '',    -- External source identifier
    url              text NOT NULL DEFAULT '',      -- External reference URL
    imported_at      timestamptz NOT NULL DEFAULT now(),  -- Local desktop import timestamp
    created_at       timestamptz NOT NULL DEFAULT now(),  -- Cloud record creation timestamp
    updated_at       timestamptz NOT NULL DEFAULT now(),  -- Cloud record update timestamp

    PRIMARY KEY (account_id, id),
    CONSTRAINT history_work_items_title_not_empty CHECK (length(trim(both from title)) > 0),
    CONSTRAINT history_work_items_title_len CHECK (length(title) <= 500),
    CONSTRAINT history_work_items_details_len CHECK (length(details) <= 20000),
    CONSTRAINT history_work_items_project_len CHECK (length(project) <= 500),
    CONSTRAINT history_work_items_source_len CHECK (length(source) <= 32),
    CONSTRAINT history_work_items_source_id_len CHECK (length(source_id) <= 200),
    CONSTRAINT history_work_items_url_len CHECK (length(url) <= 2000)
);

-- Index 1: Pagination and date filtering with NULLS LAST
CREATE INDEX IF NOT EXISTS history_work_items_date_idx
    ON history_work_items (account_id, work_date DESC NULLS LAST, id ASC);

-- Index 2: Project filtering
CREATE INDEX IF NOT EXISTS history_work_items_project_idx
    ON history_work_items (account_id, project, work_date DESC NULLS LAST);

-- Index 3: Source filtering
CREATE INDEX IF NOT EXISTS history_work_items_source_idx
    ON history_work_items (account_id, source);
