-- External logins (GitHub OAuth for now). One account may link several.
CREATE TABLE user_identities (
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL, -- provider's stable user id, never the login name
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    login      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject),
    UNIQUE (user_id, provider)
);
