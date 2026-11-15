-- The old Java app's schema (Flyway V1–V8), used to test the importer.
CREATE TABLE users (
    id  BIGSERIAL PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,
    display_name VARCHAR(128),
    bio TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE posts (
    id BIGSERIAL PRIMARY KEY,
    author_id BIGINT NOT NULL REFERENCES users(id),
    title VARCHAR(256) NOT NULL,
    slug VARCHAR(256) NOT NULL,
    body_md TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'DRAFT',
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (author_id, slug)
);
CREATE TABLE tags (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(64) NOT NULL UNIQUE,
    slug VARCHAR(64) NOT NULL UNIQUE
);
CREATE TABLE post_tags (
    post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    tag_id BIGINT NOT NULL REFERENCES tags(id) on DELETE CASCADE,
    PRIMARY KEY (post_id, tag_id)
);
CREATE TABLE series (
    id BIGSERIAL PRIMARY KEY,
    author_id BIGINT NOT NULL REFERENCES users(id),
    title VARCHAR(256) NOT NULL,
    slug VARCHAR(256) NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (author_id, slug)
);
CREATE TABLE post_series (
    post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    series_id BIGINT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    position INT NOT NULL,
    PRIMARY KEY (post_id, series_id)
);
ALTER TABLE posts ADD COLUMN description VARCHAR(300);
ALTER TABLE posts ADD COLUMN cover_image_url VARCHAR(512);
ALTER TABLE users ADD COLUMN about_md TEXT;
ALTER TABLE posts ADD COLUMN view_count BIGINT NOT NULL DEFAULT 0;
