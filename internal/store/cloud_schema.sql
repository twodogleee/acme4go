-- 云证书上传与部署分离；任务不保存私钥或明文凭证。
CREATE TABLE IF NOT EXISTS cloud_plans (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
 series_id TEXT REFERENCES certificate_series(id),
 name TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('UPLOAD','DEPLOY')),
 product TEXT NOT NULL DEFAULT '',
 remote_certificate_id TEXT NOT NULL DEFAULT '',
 config_json TEXT NOT NULL DEFAULT '{}',
 auto_sync INTEGER NOT NULL DEFAULT 0,
 enabled INTEGER NOT NULL DEFAULT 1,
 revision INTEGER NOT NULL DEFAULT 1,
 deleted INTEGER NOT NULL DEFAULT 0,
 CHECK ((series_id IS NOT NULL AND remote_certificate_id='') OR (series_id IS NULL AND remote_certificate_id<>'')),
 CHECK (kind<>'UPLOAD' OR (series_id IS NOT NULL AND product='')),
 CHECK (auto_sync=0 OR series_id IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_product_target ON cloud_plans(credential_id,product,config_json) WHERE kind='DEPLOY' AND deleted=0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_upload_series ON cloud_plans(credential_id,series_id) WHERE kind='UPLOAD' AND deleted=0;
CREATE TABLE IF NOT EXISTS cloud_uploads (
 credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
 fingerprint TEXT NOT NULL,
 remote_certificate_id TEXT NOT NULL,
 PRIMARY KEY(credential_id,fingerprint)
);
CREATE TABLE IF NOT EXISTS cloud_jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 plan_id INTEGER NOT NULL REFERENCES cloud_plans(id),
 revision INTEGER NOT NULL,
 certificate_id INTEGER REFERENCES certificates(id),
 remote_certificate_id TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('QUEUED','RUNNING','SUCCESS','FAILED')),
 stage TEXT NOT NULL DEFAULT 'QUEUED',
 message TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_job_active ON cloud_jobs(plan_id) WHERE status IN ('QUEUED','RUNNING');
CREATE INDEX IF NOT EXISTS idx_cloud_job_history ON cloud_jobs(plan_id,revision,id DESC);
