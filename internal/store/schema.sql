-- 演示项目的 schema：启动时 CREATE TABLE IF NOT EXISTS，不做迁移。
-- 时间统一用 Unix 毫秒（UTC）存储。

CREATE TABLE IF NOT EXISTS players (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    deleted_at INTEGER
);

-- 名字唯一性只在未删除范围内成立：软删除后可以用同名重建玩家。
CREATE UNIQUE INDEX IF NOT EXISTS ux_players_name_alive
    ON players (name) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS revisions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    operations TEXT    NOT NULL,
    snapshot   TEXT    NOT NULL,
    note       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    player_id       INTEGER NOT NULL,
    text            TEXT    NOT NULL,
    normalized_text TEXT    NOT NULL,
    truncated       INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_messages_player ON messages (player_id, created_at);
CREATE INDEX IF NOT EXISTS ix_messages_created ON messages (created_at);

CREATE TABLE IF NOT EXISTS judgments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id   INTEGER NOT NULL,
    player_id    INTEGER NOT NULL,
    severity     INTEGER NOT NULL,
    is_violation INTEGER NOT NULL,
    disposition  TEXT    NOT NULL,
    rung         INTEGER NOT NULL,
    duration_ms  INTEGER NOT NULL,
    permanent    INTEGER NOT NULL DEFAULT 0,
    hit_count    INTEGER NOT NULL DEFAULT 0,
    hits         TEXT    NOT NULL DEFAULT '[]',
    created_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_judgments_player
    ON judgments (player_id, created_at, is_violation);

CREATE TABLE IF NOT EXISTS mutes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    player_id  INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    expires_at INTEGER,           -- NULL = 永久
    ended_at   INTEGER,           -- NULL = 仍活跃
    end_reason TEXT,              -- expired / lifted / replaced
    lift_note  TEXT
);

CREATE INDEX IF NOT EXISTS ix_mutes_player ON mutes (player_id, ended_at);

-- 每个玩家至多一条活跃禁言：接口与 UI 都不可能造出两条。
CREATE UNIQUE INDEX IF NOT EXISTS ux_mutes_one_active
    ON mutes (player_id) WHERE ended_at IS NULL;
