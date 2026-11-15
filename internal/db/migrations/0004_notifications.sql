CREATE TABLE notifications (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE, -- recipient
    kind       TEXT NOT NULL CHECK (kind IN ('thread_reply', 'comment_reply')),
    actor_id   BIGINT REFERENCES users (id) ON DELETE SET NULL,
    thread_id  BIGINT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    comment_id BIGINT NOT NULL REFERENCES comments (id) ON DELETE CASCADE,
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notifications_unread_idx ON notifications (user_id, id DESC) WHERE read_at IS NULL;
CREATE INDEX notifications_user_idx ON notifications (user_id, id DESC);
