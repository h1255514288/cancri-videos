-- 视频分发系统数据库初始化脚本

CREATE TABLE IF NOT EXISTS admins (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(64) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_groups (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(128) UNIQUE NOT NULL,
    description TEXT,
    default_daily_quota INT,
    default_max_pending_claims INT,
    default_max_concurrent_downloads INT,
    default_download_speed_limit_mbps INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT REFERENCES user_groups(id) ON DELETE SET NULL,
    internal_name VARCHAR(128),
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'blacklisted')),
    blacklist_reason TEXT,
    blacklisted_at TIMESTAMPTZ,
    blacklisted_by BIGINT REFERENCES admins(id) ON DELETE SET NULL,
    daily_quota INT,
    max_pending_claims INT,
    max_concurrent_downloads INT,
    download_speed_limit_mbps INT,
    total_claims INT NOT NULL DEFAULT 0,
    last_claim_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_users_group ON users(group_id);
CREATE INDEX idx_users_status ON users(status);

CREATE TABLE IF NOT EXISTS api_keys (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_hash VARCHAR(64) UNIQUE NOT NULL,
    key_prefix VARCHAR(16) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    effective_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoked_by BIGINT REFERENCES admins(id) ON DELETE SET NULL,
    revoke_reason TEXT,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_api_keys_user ON api_keys(user_id);
CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX idx_api_keys_status ON api_keys(status);

CREATE TABLE IF NOT EXISTS categories (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(128) UNIQUE NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_categories_status ON categories(status);

CREATE TABLE IF NOT EXISTS user_category_permissions (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id BIGINT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    granted_by BIGINT REFERENCES admins(id) ON DELETE SET NULL,
    PRIMARY KEY (user_id, category_id)
);
CREATE INDEX idx_user_category_category ON user_category_permissions(category_id);

CREATE TABLE IF NOT EXISTS storage_nodes (
    id BIGSERIAL PRIMARY KEY,
    node_name VARCHAR(128) UNIQUE NOT NULL,
    storage_root_path TEXT NOT NULL,
    download_base_url TEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'readonly', 'offline')),
    total_capacity_bytes BIGINT,
    used_bytes BIGINT NOT NULL DEFAULT 0,
    total_videos INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS videos (
    id BIGSERIAL PRIMARY KEY,
    video_id VARCHAR(32) UNIQUE NOT NULL,
    category_id BIGINT NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
    node_id BIGINT NOT NULL REFERENCES storage_nodes(id) ON DELETE RESTRICT,
    title VARCHAR(255),
    filename VARCHAR(255) NOT NULL,
    storage_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    sha256 VARCHAR(64) NOT NULL,
    mime_type VARCHAR(64),
    status VARCHAR(32) NOT NULL DEFAULT 'uploading' 
        CHECK (status IN ('uploading', 'available', 'claimed', 'downloading', 'consumed', 'pending_deletion', 'deleted', 'error')),
    uploaded_by BIGINT REFERENCES admins(id) ON DELETE SET NULL,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    first_download_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    error_message TEXT
);
CREATE INDEX idx_videos_category_status ON videos(category_id, status);
CREATE INDEX idx_videos_status ON videos(status);
CREATE INDEX idx_videos_node ON videos(node_id);
CREATE INDEX idx_videos_uploaded_at ON videos(uploaded_at);

CREATE TABLE IF NOT EXISTS claims (
    id BIGSERIAL PRIMARY KEY,
    claim_id VARCHAR(32) UNIQUE NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    video_id BIGINT NOT NULL REFERENCES videos(id) ON DELETE RESTRICT,
    idempotency_key VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'reserved'
        CHECK (status IN ('reserved', 'streaming', 'retryable', 'completed', 'deleting')),
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_expires_at TIMESTAMPTZ NOT NULL,
    first_download_at TIMESTAMPTZ,
    retry_deadline TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    credential_version INT NOT NULL DEFAULT 1,
    retry_count INT NOT NULL DEFAULT 0,
    UNIQUE(user_id, idempotency_key)
);
CREATE INDEX idx_claims_user ON claims(user_id);
CREATE INDEX idx_claims_video ON claims(video_id);
CREATE INDEX idx_claims_status ON claims(status);
CREATE INDEX idx_claims_expires ON claims(claim_expires_at) WHERE status = 'reserved';

CREATE TABLE IF NOT EXISTS download_attempts (
    id BIGSERIAL PRIMARY KEY,
    attempt_id VARCHAR(32) UNIQUE NOT NULL,
    claim_id BIGINT NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
    node_id BIGINT NOT NULL REFERENCES storage_nodes(id) ON DELETE RESTRICT,
    credential_version INT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ,
    bytes_sent BIGINT NOT NULL DEFAULT 0,
    result VARCHAR(32) CHECK (result IN ('completed', 'client_disconnect', 'error', 'timeout')),
    error_message TEXT,
    client_ip VARCHAR(64),
    user_agent TEXT
);
CREATE INDEX idx_download_attempts_claim ON download_attempts(claim_id);
CREATE INDEX idx_download_attempts_started ON download_attempts(started_at);

CREATE TABLE IF NOT EXISTS daily_quotas (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    quota_date DATE NOT NULL,
    used_count INT NOT NULL DEFAULT 0,
    last_claim_at TIMESTAMPTZ,
    UNIQUE(user_id, quota_date)
);
CREATE INDEX idx_daily_quotas_user_date ON daily_quotas(user_id, quota_date);

CREATE TABLE IF NOT EXISTS deletion_jobs (
    id BIGSERIAL PRIMARY KEY,
    video_id BIGINT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    node_id BIGINT NOT NULL REFERENCES storage_nodes(id) ON DELETE RESTRICT,
    storage_path TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 5,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX idx_deletion_jobs_status_next ON deletion_jobs(status, next_attempt_at);
CREATE INDEX idx_deletion_jobs_video ON deletion_jobs(video_id);

CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL PRIMARY KEY,
    event_type VARCHAR(64) NOT NULL,
    actor_type VARCHAR(32) NOT NULL CHECK (actor_type IN ('admin', 'user', 'system')),
    actor_id BIGINT,
    target_type VARCHAR(32),
    target_id BIGINT,
    details JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_logs_created ON audit_logs(created_at);
CREATE INDEX idx_audit_logs_event ON audit_logs(event_type);
CREATE INDEX idx_audit_logs_actor ON audit_logs(actor_type, actor_id);

-- 插入默认分类
INSERT INTO categories (name) VALUES ('default') ON CONFLICT (name) DO NOTHING;

-- 插入默认存储节点
INSERT INTO storage_nodes (node_name, storage_root_path, download_base_url, total_capacity_bytes)
VALUES ('node-1', '/var/video-distribution/storage', 'http://localhost:8001', 1099511627776)
ON CONFLICT (node_name) DO NOTHING;
