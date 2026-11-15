-- Trigram index support for substring search (also covers CJK text,
-- which the english text-search config doesn't segment).
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9_]{3,32}$'),
    display_name  TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 64),
    password_hash TEXT NOT NULL,
    about_md      TEXT NOT NULL DEFAULT '' CHECK (octet_length(about_md) <= 50000),
    role          TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'admin')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only a SHA-256 of the session token is stored, so a leaked table
-- can't be replayed as cookies.
CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE series (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id   BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    slug        TEXT NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 120),
    title       TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 300),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (author_id, slug)
);

CREATE TABLE posts (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    slug         TEXT NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 120),
    title        TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    description  TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 300),
    -- Media is never stored here: covers are external https links only.
    cover_url    TEXT NOT NULL DEFAULT '' CHECK (cover_url = '' OR (cover_url LIKE 'https://%' AND length(cover_url) <= 1000)),
    body_md      TEXT NOT NULL DEFAULT '' CHECK (octet_length(body_md) <= 200000),
    status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    series_id    BIGINT REFERENCES series (id) ON DELETE SET NULL,
    series_pos   INT,
    view_count   BIGINT NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    search_doc   TSVECTOR GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title), 'A') ||
        setweight(to_tsvector('english', description), 'B') ||
        to_tsvector('english', body_md)
    ) STORED,
    UNIQUE (author_id, slug),
    CHECK (status = 'draft' OR published_at IS NOT NULL)
);
CREATE INDEX posts_published_idx ON posts (published_at DESC) WHERE status = 'published';
CREATE INDEX posts_author_published_idx ON posts (author_id, published_at DESC) WHERE status = 'published';
CREATE INDEX posts_series_idx ON posts (series_id, series_pos) WHERE series_id IS NOT NULL;
CREATE INDEX posts_search_idx ON posts USING GIN (search_doc);
CREATE INDEX posts_trgm_idx ON posts USING GIN ((title || ' ' || description || ' ' || body_md) gin_trgm_ops);

CREATE TABLE tags (
    id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 64),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64)
);

CREATE TABLE post_tags (
    post_id BIGINT NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    tag_id  BIGINT NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (post_id, tag_id)
);
CREATE INDEX post_tags_tag_idx ON post_tags (tag_id);
