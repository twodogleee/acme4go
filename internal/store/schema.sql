-- Acme4go 新数据库的权威完整 Schema。
-- SQLite 不保存字段 COMMENT 元数据，因此表、字段、约束和索引说明使用源码注释维护。
-- 已有数据库的兼容升级由 store.Open 调用带版本记录的 Go 幂等迁移函数负责，不执行 migrations 目录中的 SQL。

-- 为当前连接启用外键约束；每个 SQLite 连接都必须单独开启。
PRAGMA foreign_keys = ON;

-- 管理员账户表，只保存密码摘要，不保存明文密码。
CREATE TABLE IF NOT EXISTS users (
    -- 管理员数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 管理端登录用户名，在当前控制平面内唯一。
    username TEXT NOT NULL UNIQUE,
    -- 不可逆算法生成的密码摘要。
    password_hash TEXT NOT NULL,
    -- 管理端展示名称。
    display_name TEXT NOT NULL,
    -- 账户创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- 最近一次成功登录时间；从未登录时为空。
    last_login_at TEXT
);

-- 管理端登录会话表，只保存令牌和 CSRF 令牌的摘要。
CREATE TABLE IF NOT EXISTS auth_sessions (
    -- 会话令牌 SHA-256 摘要，同时作为会话主键。
    token_hash TEXT PRIMARY KEY,
    -- CSRF 令牌 SHA-256 摘要。
    csrf_hash TEXT NOT NULL,
    -- 会话所属管理员；删除管理员时级联删除其全部会话。
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- 会话失效时间，使用 UTC RFC3339Nano 文本。
    expires_at TEXT NOT NULL,
    -- 会话创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL
);

-- DNS-01 验证凭据表，敏感字段使用部署主密钥加密后保存。
CREATE TABLE IF NOT EXISTS dns_credentials (
    -- DNS 凭据数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 管理端展示的凭据名称。
    name TEXT NOT NULL,
    -- DNS 服务商类型，支持阿里云及 Cloudflare API Token。
    provider TEXT NOT NULL CHECK (provider IN ('ALIYUN','CLOUDFLARE')),
    -- 首个根域名，兼容旧接口；完整范围使用 zones_json。
    zone TEXT NOT NULL,
    -- DNS 验证的完整根域名列表，空数组不授予任何 DNS 范围。
    zones_json TEXT NOT NULL DEFAULT '[]',
    -- 最近一次逐根域名的安全测试结果；修改凭据后清空。
    last_test_results_json TEXT NOT NULL DEFAULT '[]',
    -- 使用部署主密钥加密保存的 AccessKeyId 密文。
    access_key_id_enc TEXT NOT NULL,
    -- 使用部署主密钥加密保存的 AccessKeySecret 密文。
    access_key_secret_enc TEXT NOT NULL,
    -- 是否允许新订单和自动续期使用；1 表示启用，0 表示停用。
    enabled INTEGER NOT NULL DEFAULT 1,
    -- 最近一次连通性测试状态；尚未测试时为空。
    last_test_status TEXT,
    -- 最近一次连通性测试的安全结果说明，不包含 Secret 明文。
    last_test_message TEXT,
    -- 最近一次连通性测试时间，使用 UTC RFC3339Nano 文本。
    last_test_at TEXT,
    -- 凭据创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- 凭据最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL
);

-- ACME 账户表，按联系邮箱和 CA Directory 复用账户私钥及注册资源。
CREATE TABLE IF NOT EXISTS acme_accounts (
    -- 邮箱与 CA Directory 组合生成的稳定账户键。
    account_key TEXT PRIMARY KEY,
    -- ACME 账户联系邮箱。
    email TEXT NOT NULL,
    -- ACME 服务端 Directory 地址，用于隔离生产与测试账户。
    ca_directory TEXT NOT NULL,
    -- 使用部署主密钥加密保存的 ACME 账户私钥。
    private_key_enc TEXT NOT NULL,
    -- ACME 服务端账户注册资源 JSON；注册完成前可以为空。
    registration_json TEXT,
    -- 账户材料最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL
);

-- 证书系列表，一个系列代表固定 CA 下的一组固定域名及其连续签发版本。
CREATE TABLE IF NOT EXISTS certificate_series (
    -- 系列不可变稳定标识，是订单、证书版本和部署目标的统一关联键。
    id TEXT PRIMARY KEY,
    -- 面向管理员展示和沟通的唯一系列编码。
    code TEXT NOT NULL UNIQUE,
    -- 管理端展示的系列名称。
    name TEXT NOT NULL,
    -- 系列覆盖的完整域名数组，使用 JSON 文本保存。
    domains_json TEXT NOT NULL,
    -- 规范化无序域名集合的 SHA-256 摘要，用于识别相同域名组合。
    domains_hash TEXT NOT NULL,
    -- 自动续期使用的 ACME 联系邮箱。
    email TEXT NOT NULL,
    -- 管理端使用的 CA 环境标识，例如 LETS_ENCRYPT_PRODUCTION。
    ca_environment TEXT NOT NULL,
    -- 实际 ACME Directory 地址，与域名摘要共同构成系列自然键。
    ca_directory TEXT NOT NULL,
    -- 自动签发和续期使用的 DNS 凭据主键。
    credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
    -- 当前可部署的物理证书主键；尚未签发成功或证书被删除时为空。
    current_certificate_id INTEGER REFERENCES certificates(id) ON DELETE SET NULL,
    -- 是否自动创建续期订单；1 表示启用，0 表示关闭。
    auto_renew INTEGER NOT NULL DEFAULT 1,
    -- 系列是否允许继续签发和部署；1 表示启用，0 表示停用。
    enabled INTEGER NOT NULL DEFAULT 1,
    -- 系列配置修订号，续期配置变更时递增。
    revision INTEGER NOT NULL DEFAULT 1,
    -- 系列创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- 系列最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL,
    -- 相同 CA Directory 下，同一无序域名组合只能存在一个系列。
    UNIQUE(ca_directory, domains_hash)
);

-- 物理证书版本表，每次成功签发生成一条不可变版本记录。
CREATE TABLE IF NOT EXISTS certificates (
    -- 物理证书数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 证书所属稳定系列；兼容迁移期间允许为空。
    series_id TEXT REFERENCES certificate_series(id),
    -- 证书主题通用名称，主要用于展示和旧数据兼容，不作为部署关联键。
    common_name TEXT NOT NULL,
    -- 证书覆盖的完整 DNS SAN 数组，使用 JSON 文本保存。
    sans_json TEXT NOT NULL,
    -- 证书签发者名称。
    issuer TEXT NOT NULL,
    -- CA 分配的证书序列号。
    serial_number TEXT NOT NULL,
    -- 证书生效时间，使用 UTC RFC3339Nano 文本。
    not_before TEXT NOT NULL,
    -- 证书到期时间，使用 UTC RFC3339Nano 文本。
    not_after TEXT NOT NULL,
    -- 叶证书 DER 内容的 SHA-256 指纹，在证书库内唯一。
    fingerprint TEXT NOT NULL UNIQUE,
    -- 证书在所属系列内的递增版本号。
    version INTEGER NOT NULL,
    -- 加密叶证书在内部证书库中的存储路径。
    cert_path TEXT NOT NULL,
    -- 加密完整证书链在内部证书库中的存储路径。
    chain_path TEXT NOT NULL,
    -- 加密私钥在内部证书库中的存储路径。
    key_path TEXT NOT NULL,
    -- 本次签发使用的 ACME 联系邮箱，保留用于历史审计。
    email TEXT NOT NULL,
    -- 本次签发使用的 ACME Directory 地址。
    ca_directory TEXT NOT NULL,
    -- 本次签发使用的 DNS 凭据主键。
    credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
    -- 证书记录创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- 证书记录最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL
);

-- ACME 签发订单表，保存一次申请或自动续期的完整执行状态。
CREATE TABLE IF NOT EXISTS certificate_orders (
    -- 证书订单数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 订单所属稳定证书系列；兼容迁移期间允许为空。
    series_id TEXT REFERENCES certificate_series(id),
    -- 本次订单申请的域名数组，使用 JSON 文本保存。
    domains_json TEXT NOT NULL,
    -- 本次订单使用的 ACME 联系邮箱。
    email TEXT NOT NULL,
    -- 管理端使用的 CA 环境标识。
    ca_environment TEXT NOT NULL,
    -- 实际 ACME Directory 地址。
    ca_directory TEXT NOT NULL,
    -- DNS-01 验证使用的 DNS 凭据主键。
    credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),
    -- 订单总体状态，例如 QUEUED、RUNNING、SUCCESS 或 FAILED。
    status TEXT NOT NULL,
    -- 订单当前细分阶段，例如 AUTHORIZING_DNS、ISSUING 或 STORING。
    stage TEXT NOT NULL,
    -- 订单失败时可安全展示的错误说明；未失败时为空。
    error_message TEXT,
    -- 成功签发后关联的物理证书主键；完成前为空。
    certificate_id INTEGER REFERENCES certificates(id),
    -- 人工申请的管理员主键；自动续期订单为空。
    requested_by INTEGER REFERENCES users(id),
    -- 订单开始执行时间，使用 UTC RFC3339Nano 文本；尚未开始时为空。
    started_at TEXT,
    -- 订单完成或失败时间，使用 UTC RFC3339Nano 文本；未结束时为空。
    completed_at TEXT,
    -- 订单创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- 订单最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL
);

-- 已注册部署 Agent 身份表，身份由内部 CA 客户端证书绑定。
CREATE TABLE IF NOT EXISTS agents (
    -- Agent 不可变 UUID，同时写入客户端证书通用名称。
    id TEXT PRIMARY KEY,
    -- 管理端展示的 Agent 名称。
    name TEXT NOT NULL,
    -- 最近持久化的节点状态；接口会结合心跳时间重新计算在线状态。
    status TEXT NOT NULL DEFAULT 'OFFLINE',
    -- 是否允许该 Agent 继续认证和拉取证书；1 表示启用，0 表示撤销。
    enabled INTEGER NOT NULL DEFAULT 1,
    -- 当前有效客户端证书序列号，在全部 Agent 中唯一。
    certificate_serial TEXT NOT NULL UNIQUE,
    -- 证书轮换恢复窗口内允许使用的上一序列号。
    previous_certificate_serial TEXT UNIQUE,
    -- 当前客户端证书到期时间，使用 UTC RFC3339Nano 文本。
    certificate_expires_at TEXT NOT NULL,
    -- 最近一次认证请求或心跳时间，使用 UTC RFC3339Nano 文本。
    last_seen_at TEXT,
    -- Agent 上报的应用版本。
    version TEXT,
    -- Agent 上报的操作系统与架构信息。
    platform TEXT,
    -- Agent 注册时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- Agent 信息最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL
);

-- Agent 一次性注册令牌表，数据库只保存令牌摘要。
CREATE TABLE IF NOT EXISTS enrollment_tokens (
    -- 一次性 Join Token 的 SHA-256 摘要。
    token_hash TEXT PRIMARY KEY,
    -- 使用该令牌创建的 Agent 显示名称。
    name TEXT NOT NULL,
    -- 令牌失效时间，使用 UTC RFC3339Nano 文本。
    expires_at TEXT NOT NULL,
    -- 令牌实际使用时间；尚未使用时为空。
    used_at TEXT,
    -- 令牌创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL
);

-- 文件部署目标表，描述 Server 或指定 Agent 应维护的稳定 current 路径。
CREATE TABLE IF NOT EXISTS deployment_targets (
    -- 部署目标数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 执行器类型；LOCAL 表示 Server 本机，AGENT 表示远程节点。
    executor_type TEXT NOT NULL CHECK (executor_type IN ('LOCAL', 'AGENT')),
    -- 远程执行 Agent 标识；LOCAL 目标必须为空，AGENT 目标必须有值。
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    -- 管理端展示的部署目标名称。
    name TEXT NOT NULL,
    -- 人工绑定的稳定证书系列标识；旧目标迁移完成前可以为空。
    certificate_series_id TEXT REFERENCES certificate_series(id),
    -- 旧版按域名匹配时使用的兼容字段，不再作为部署授权关联键。
    common_name TEXT NOT NULL,
    -- 部署根目录下的安全目录别名，在同一执行器内唯一。
    target_alias TEXT NOT NULL,
    -- 是否自动跟随系列当前证书；1 表示启用，0 表示暂停。
    enabled INTEGER NOT NULL DEFAULT 1,
    -- 目标期望配置修订号，影响 Agent 对账状态键。
    revision INTEGER NOT NULL DEFAULT 1,
    -- 最近部署状态，例如 PENDING、SUCCESS、FAILED 或 NEEDS_BINDING。
    status TEXT NOT NULL DEFAULT 'PENDING',
    -- 最近一次尝试部署的物理证书主键；证书删除时置空。
    last_certificate_id INTEGER REFERENCES certificates(id) ON DELETE SET NULL,
    -- 最近一次尝试部署的证书 SHA-256 指纹。
    last_fingerprint TEXT,
    -- 最近一次成功部署时间，使用 UTC RFC3339Nano 文本。
    last_deployed_at TEXT,
    -- 最近一次部署失败或需要重新绑定的安全说明。
    last_error TEXT,
    -- 部署目标创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL,
    -- 部署目标最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL,
    -- 本机目标不得绑定 Agent，远程目标必须绑定一个 Agent。
    CHECK (
        (executor_type = 'LOCAL' AND agent_id IS NULL) OR
        (executor_type = 'AGENT' AND agent_id IS NOT NULL)
    )
);

-- 部署结果审计表，记录本机执行器和 Agent 的证书切换结果。
CREATE TABLE IF NOT EXISTS deployment_records (
    -- 部署记录数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 对应部署目标；目标配置删除后保留记录并将该字段置空。
    target_id INTEGER REFERENCES deployment_targets(id) ON DELETE SET NULL,
    -- 实际执行器类型，只允许 LOCAL 或 AGENT。
    executor_type TEXT NOT NULL CHECK (executor_type IN ('LOCAL', 'AGENT')),
    -- 实际执行的 Agent 标识；本机执行记录为空，Agent 删除后置空。
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    -- 本次部署的物理证书主键；证书删除后置空。
    certificate_id INTEGER REFERENCES certificates(id) ON DELETE SET NULL,
    -- 本次部署证书的 SHA-256 指纹，用于记录删除后的审计追溯。
    fingerprint TEXT,
    -- 部署结果状态，例如 SUCCESS 或 FAILED。
    status TEXT NOT NULL,
    -- 部署结果说明，不包含私钥、令牌或凭据明文。
    message TEXT NOT NULL,
    -- 部署记录创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL
);

-- 管理操作审计日志表，不记录密码、令牌、私钥或 DNS Secret 明文。
CREATE TABLE IF NOT EXISTS audit_logs (
    -- 审计日志数据库自增主键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 执行操作的管理员主键；管理员删除后置空。
    user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    -- 审计动作编码，例如 CREATE、UPDATE、DELETE 或 REVOKE。
    action TEXT NOT NULL,
    -- 被操作资源类型，例如 CERTIFICATE_ORDER 或 DEPLOYMENT_TARGET。
    resource_type TEXT NOT NULL,
    -- 被操作资源的稳定标识；无具体资源时为空。
    resource_id TEXT,
    -- 不含敏感明文的操作摘要。
    detail TEXT,
    -- 审计日志创建时间，使用 UTC RFC3339Nano 文本。
    created_at TEXT NOT NULL
);

-- 小型非敏感系统设置表；秘密配置仍必须通过环境变量或 Secret 注入。
CREATE TABLE IF NOT EXISTS system_settings (
    -- 设置项唯一键。
    setting_key TEXT PRIMARY KEY,
    -- 设置项文本值，不允许保存密码、私钥、令牌或 DNS Secret。
    setting_value TEXT NOT NULL,
    -- 设置项最后更新时间，使用 UTC RFC3339Nano 文本。
    updated_at TEXT NOT NULL
);

-- 订单执行时间线，只保存系统生成的阶段说明和安全失败摘要。
CREATE TABLE IF NOT EXISTS order_events (
    -- 时间线事件自增标识，作为同一时间事件的稳定排序键。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 所属订单，订单保留期清理时同步删除事件。
    order_id INTEGER NOT NULL REFERENCES certificate_orders(id) ON DELETE CASCADE,
    -- 执行阶段编码。
    stage TEXT NOT NULL,
    -- 不含凭据和第三方原始响应的说明。
    message TEXT NOT NULL,
    -- 事件 UTC 时间。
    created_at TEXT NOT NULL
);

-- Webhook 持久投递队列，既保存待发送事件也供管理端查询历史。
CREATE TABLE IF NOT EXISTS alert_deliveries (
    -- 投递标识，同时作为接收端去重使用的事件 ID。
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 可选事件去重键，普通业务事件允许为空。
    dedupe_key TEXT UNIQUE,
    -- 稳定事件类型。
    event TEXT NOT NULL,
    -- 告警级别，ERROR、WARNING 或 INFO。
    severity TEXT NOT NULL,
    -- 关联业务资源类型。
    resource_type TEXT NOT NULL,
    -- 关联业务资源稳定标识。
    resource_id TEXT NOT NULL,
    -- 不含秘密的通知正文。
    message TEXT NOT NULL,
    -- PENDING、SENDING、RETRY、SENT 或 FAILED。
    status TEXT NOT NULL DEFAULT 'PENDING',
    -- 当前一轮投递已经尝试的次数，人工重发时归零。
    attempts INTEGER NOT NULL DEFAULT 0,
    -- 下次投递时间；SENDING 时是工作租约到期时间。
    next_attempt_at TEXT NOT NULL,
    -- 最近失败的安全摘要。
    last_error TEXT,
    -- 投递成功的 UTC 时间。
    delivered_at TEXT,
    -- 原始事件发生时间，重试期间保持不变。
    created_at TEXT NOT NULL,
    -- 投递记录最后更新时间。
    updated_at TEXT NOT NULL
);

-- Go 自动迁移版本表，只记录已经完整成功的迁移，不保存执行中的中间状态。
CREATE TABLE IF NOT EXISTS schema_migrations (
    -- 单调递增的 Go 迁移版本号，同时作为迁移主键。
    version INTEGER PRIMARY KEY,
    -- 稳定的迁移名称，用于日志、审计和故障定位。
    name TEXT NOT NULL,
    -- 迁移成功完成并写入版本记录的时间，使用 UTC RFC3339Nano 文本。
    applied_at TEXT NOT NULL
);

-- 加速按失效时间清理管理会话。
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON auth_sessions(expires_at);
-- 加速管理端按创建时间倒序列出最近订单。
CREATE INDEX IF NOT EXISTS idx_orders_created_at ON certificate_orders(created_at DESC);
-- 加速调度器和管理端按订单状态筛选任务。
CREATE INDEX IF NOT EXISTS idx_orders_status ON certificate_orders(status);
-- 兼容旧版按主域名查看证书版本的查询；部署关联不使用该索引。
CREATE INDEX IF NOT EXISTS idx_certificates_common_name ON certificates(common_name, version DESC);
-- 加速证书到期状态统计和续期窗口扫描。
CREATE INDEX IF NOT EXISTS idx_certificates_not_after ON certificates(not_after);
-- 加速按最近心跳判断 Agent 在线状态。
CREATE INDEX IF NOT EXISTS idx_agents_last_seen ON agents(last_seen_at);
-- 加速读取指定 Agent 的启用部署目标。
CREATE INDEX IF NOT EXISTS idx_targets_agent ON deployment_targets(agent_id, enabled) WHERE executor_type = 'AGENT';
-- 保证 Server 本机执行器内的部署目录别名唯一。
CREATE UNIQUE INDEX IF NOT EXISTS idx_targets_local_alias ON deployment_targets(target_alias) WHERE executor_type = 'LOCAL';
-- 保证同一 Agent 内的部署目录别名唯一。
CREATE UNIQUE INDEX IF NOT EXISTS idx_targets_agent_alias ON deployment_targets(agent_id, target_alias) WHERE executor_type = 'AGENT';
-- 兼容旧部署目标迁移时按主域名查找候选系列；运行时授权不使用该索引。
CREATE INDEX IF NOT EXISTS idx_targets_common_name ON deployment_targets(common_name, enabled);
-- 加速按时间倒序读取和清理部署历史记录。
CREATE INDEX IF NOT EXISTS idx_deployment_records_created ON deployment_records(created_at DESC);
-- 加速审计页面按主键游标、动作和资源类型倒序翻页。
CREATE INDEX IF NOT EXISTS idx_audit_logs_filters ON audit_logs(action, resource_type, id DESC);
-- 加速按订单读取完整阶段历史。
CREATE INDEX IF NOT EXISTS idx_order_events_order ON order_events(order_id,id);
-- 加速投递队列认领和失败记录查询。
CREATE INDEX IF NOT EXISTS idx_alert_delivery_due ON alert_deliveries(status,next_attempt_at,id);
-- 加速获取每个目标最近成功使用的证书版本。
CREATE INDEX IF NOT EXISTS idx_deployment_records_target_status ON deployment_records(target_id,status,id DESC);
