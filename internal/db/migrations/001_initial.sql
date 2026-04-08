-- Schema version: 1
-- All DATETIME columns store UTC in RFC3339 format ("2006-01-02T15:04:05Z").

CREATE TABLE checks (
    id               TEXT     PRIMARY KEY,
    name             TEXT     NOT NULL,
    slug             TEXT     UNIQUE,
    schedule         TEXT     NOT NULL,
    grace            INTEGER  NOT NULL DEFAULT 10,
    status           TEXT     NOT NULL DEFAULT 'new'
                              CHECK(status IN ('new','up','down','paused')),
    last_ping_at     DATETIME,
    next_expected_at DATETIME,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    tags             TEXT     NOT NULL DEFAULT '',
    notify_on_fail   INTEGER  NOT NULL DEFAULT 0  -- boolean (0=false, 1=true); opt-in fail alerts
);

CREATE TABLE pings (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    check_id   TEXT     NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    type       TEXT     NOT NULL CHECK(type IN ('success','start','fail')),
    created_at DATETIME NOT NULL,
    source_ip  TEXT     NOT NULL DEFAULT ''
);

CREATE TABLE channels (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    type       TEXT     NOT NULL CHECK(type IN ('email','slack','webhook')),
    name       TEXT     NOT NULL,
    config     TEXT     NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE TABLE check_channels (
    check_id   TEXT    NOT NULL REFERENCES checks(id)   ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    PRIMARY KEY (check_id, channel_id)
);

CREATE TABLE notifications (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    check_id   TEXT     NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    channel_id INTEGER           REFERENCES channels(id) ON DELETE SET NULL,
    type       TEXT     NOT NULL CHECK(type IN ('down','up','fail')),
    sent_at    DATETIME NOT NULL,
    error      TEXT
);

CREATE INDEX idx_checks_status       ON checks(status);
CREATE INDEX idx_pings_check_created ON pings(check_id, created_at DESC);
CREATE INDEX idx_notifications_check ON notifications(check_id, sent_at DESC);
