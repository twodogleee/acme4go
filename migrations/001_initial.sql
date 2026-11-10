-- 历史快照：记录项目最初的 SQLite 表结构，不是运行时迁移入口。
-- Acme4go 不扫描或执行 migrations 目录；新数据库以 internal/store/schema.sql 为准，
-- 已有数据库由 store.Open 调用的 Go 幂等迁移函数升级。
-- 本文件早于证书系列模型，仅供历史追溯，请勿在现有数据库上手工执行。

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_login_at TEXT
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash TEXT PRIMARY KEY,
    csrf_hash TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS dns_credentials (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider = 'ALIYUN'),
    zone TEXT NOT NULL,
    access_key_id_enc TEXT NOT NULL,
    access_key_secret_enc TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    last_test_status TEXT,
    last_test_message TEXT,
    last_test_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS acme_accounts (
    account_key TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    ca_directory TEXT NOT NULL,
    private_key_enc TEXT NOT NULL,
    registration_json TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS certificates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    common_name TEXT NOT NULL,
    sans_json TEXT NOT NULL,
    issuer TEXT NOT NULL,
    serial_number TEXT NOT NULL,
    not_before TEXT NOT NULL,
    not_after TEXT NOT NULL,
    fingerprint TEXT NOT NULL UNIQUE,
    version INTEGER NOT NULL,
    cert_path TEXT NOT NULL,
    chain_path TEXT NOT NULL,
    key_path TEXT NOT NULL,
    email TEXT NOT NULL,
    ca_directory TEXT NOT NULL,
    credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS certificate_orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domains_json TEXT NOT NULL,
    email TEXT NOT NULL,
    ca_environment TEXT NOT NULL,
    ca_directory TEXT NOT NULL,
    credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
    status TEXT NOT NULL,
    stage TEXT NOT NULL,
    error_message TEXT,
    certificate_id INTEGER REFERENCES certificates(id),
    requested_by INTEGER REFERENCES users(id),
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'OFFLINE',
    enabled INTEGER NOT NULL DEFAULT 1,
    certificate_serial TEXT NOT NULL UNIQUE,
    previous_certificate_serial TEXT UNIQUE,
    certificate_expires_at TEXT NOT NULL,
    last_seen_at TEXT,
    version TEXT,
    platform TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS enrollment_tokens (
    token_hash TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS deployment_targets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    executor_type TEXT NOT NULL CHECK (executor_type IN ('LOCAL', 'AGENT')),
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    common_name TEXT NOT NULL,
    target_alias TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    revision INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'PENDING',
    last_certificate_id INTEGER REFERENCES certificates(id) ON DELETE SET NULL,
    last_fingerprint TEXT,
    last_deployed_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (executor_type = 'LOCAL' AND agent_id IS NULL) OR
        (executor_type = 'AGENT' AND agent_id IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS deployment_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    target_id INTEGER REFERENCES deployment_targets(id) ON DELETE SET NULL,
    executor_type TEXT NOT NULL CHECK (executor_type IN ('LOCAL', 'AGENT')),
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    certificate_id INTEGER REFERENCES certificates(id) ON DELETE SET NULL,
    fingerprint TEXT,
    status TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT,
    detail TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS system_settings (
    setting_key TEXT PRIMARY KEY,
    setting_value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON auth_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_orders_created_at ON certificate_orders(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_status ON certificate_orders(status);
CREATE INDEX IF NOT EXISTS idx_certificates_common_name ON certificates(common_name, version DESC);
CREATE INDEX IF NOT EXISTS idx_certificates_not_after ON certificates(not_after);
CREATE INDEX IF NOT EXISTS idx_agents_last_seen ON agents(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_targets_agent ON deployment_targets(agent_id, enabled) WHERE executor_type = 'AGENT';
CREATE UNIQUE INDEX IF NOT EXISTS idx_targets_local_alias ON deployment_targets(target_alias) WHERE executor_type = 'LOCAL';
CREATE UNIQUE INDEX IF NOT EXISTS idx_targets_agent_alias ON deployment_targets(agent_id, target_alias) WHERE executor_type = 'AGENT';
CREATE INDEX IF NOT EXISTS idx_targets_common_name ON deployment_targets(common_name, enabled);
CREATE INDEX IF NOT EXISTS idx_deployment_records_created ON deployment_records(created_at DESC);
