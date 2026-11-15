-- Cached sum of rep_events.delta; the ledger is the source of truth
-- (`plurb rep-rebuild` recomputes this column from it).
ALTER TABLE users ADD COLUMN reputation INT NOT NULL DEFAULT 0;

CREATE TABLE threads (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id        BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title            TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body_md          TEXT NOT NULL CHECK (octet_length(body_md) <= 50000),
    -- Sum of value * weight over votes, maintained in the vote transaction.
    score            DOUBLE PRECISION NOT NULL DEFAULT 0,
    comment_count    INT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    search_doc       TSVECTOR GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title), 'A') || to_tsvector('english', body_md)
    ) STORED
);
CREATE INDEX threads_created_idx ON threads (created_at DESC);
CREATE INDEX threads_score_idx ON threads (score DESC);
CREATE INDEX threads_search_idx ON threads USING GIN (search_doc);
CREATE INDEX threads_trgm_idx ON threads USING GIN ((title || ' ' || body_md) gin_trgm_ops);

CREATE TABLE comments (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    thread_id  BIGINT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    parent_id  BIGINT REFERENCES comments (id) ON DELETE CASCADE,
    author_id  BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body_md    TEXT NOT NULL CHECK (length(body_md) BETWEEN 1 AND 20000),
    score      DOUBLE PRECISION NOT NULL DEFAULT 0,
    depth      INT NOT NULL DEFAULT 0 CHECK (depth BETWEEN 0 AND 8),
    -- Materialised path of zero-padded ancestor ids ("0000000012/0000000034"):
    -- ordering by it yields a depth-first tree in one indexed query.
    path       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX comments_thread_path_idx ON comments (thread_id, path);

CREATE TABLE votes (
    voter_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    target_type TEXT NOT NULL CHECK (target_type IN ('thread', 'comment')),
    target_id   BIGINT NOT NULL,
    value       SMALLINT NOT NULL CHECK (value IN (-1, 1)),
    -- The voter's weight when they voted. Snapshotting means later
    -- reputation changes never cascade into recomputing old scores.
    weight      DOUBLE PRECISION NOT NULL CHECK (weight >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (voter_id, target_type, target_id)
);
CREATE INDEX votes_target_idx ON votes (target_type, target_id);

-- Append-only reputation ledger. A retracted vote adds a compensating row
-- instead of editing history.
CREATE TABLE rep_events (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id        BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    delta          INT NOT NULL CHECK (delta <> 0),
    reason         TEXT NOT NULL,
    source_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    target_type    TEXT,
    target_id      BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX rep_events_user_idx ON rep_events (user_id, created_at);
CREATE INDEX rep_events_source_idx ON rep_events (source_user_id, target_type, target_id);

CREATE FUNCTION rep_events_append_only() RETURNS trigger AS $$
BEGIN
    -- Allow the FK's ON DELETE SET NULL; reject any other rewrite.
    IF NEW.id = OLD.id AND NEW.user_id = OLD.user_id AND NEW.delta = OLD.delta
       AND NEW.reason = OLD.reason AND NEW.source_user_id IS NULL THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'rep_events is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER rep_events_no_update BEFORE UPDATE ON rep_events
    FOR EACH ROW EXECUTE FUNCTION rep_events_append_only();
