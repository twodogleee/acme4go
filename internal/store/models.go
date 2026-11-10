// Package store 定义控制平面的 SQLite 持久化模型和查询边界。
//
// @author Andersen
package store

import "time"

const (
	// ProductionDirectory 是 Let's Encrypt 生产环境目录地址。
	ProductionDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	// StagingDirectory 是 Let's Encrypt 测试环境目录地址。
	StagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
	// ExecutorLocal 表示由控制平面本机执行部署。
	ExecutorLocal = "LOCAL"
	// ExecutorAgent 表示由远程 Agent 执行部署。
	ExecutorAgent = "AGENT"
)

// User 描述可以登录控制平面的管理员账户。
type User struct {
	ID          int64      `json:"id"`          // ID 是用户的数据库主键。
	Username    string     `json:"username"`    // Username 是登录用户名。
	DisplayName string     `json:"displayName"` // DisplayName 是界面显示名称。
	Password    string     `json:"-"`           // Password 是不对外序列化的密码哈希。
	CreatedAt   time.Time  `json:"createdAt"`   // CreatedAt 是账户创建时间。
	LastLoginAt *time.Time `json:"lastLoginAt"` // LastLoginAt 是最近一次成功登录时间。
}

// DNSCredential 保留 DNS 模型名称，统一承载 DNS 与云部署的加密凭据。
type DNSCredential struct {
	DNSEnabled        bool `json:"dnsEnabled"`        // DNSEnabled 允许 DNS 验证用途。
	CloudEnabled      bool `json:"cloudEnabled"`      // CloudEnabled 允许阿里云证书上传及部署用途。
	AccessKeyIDLocked bool `json:"accessKeyIdLocked"` // AccessKeyIDLocked 表示云身份已固定，不允许更换 AccessKeyId。

	ID                int64               `json:"id"`                // ID 是凭据的数据库主键。
	Name              string              `json:"name"`              // Name 是凭据的可读名称。
	Provider          string              `json:"provider"`          // Provider 是 DNS 服务商类型。
	Zone              string              `json:"zone"`              // Zone 是首个根域名，保留旧接口兼容。
	Zones             []string            `json:"zones"`             // Zones 是 DNS 验证允许使用的全部根域名。
	LastTestResults   []DNSZoneTestResult `json:"lastTestResults"`   // LastTestResults 是最近逐域名权限测试的安全结果。
	AccessKeyID       string              `json:"-"`                 // AccessKeyID 是解密后的访问密钥标识。
	AccessKeySecret   string              `json:"-"`                 // AccessKeySecret 是解密后的访问密钥秘密。
	MaskedAccessKeyID string              `json:"maskedAccessKeyId"` // MaskedAccessKeyID 是供界面展示的脱敏标识。
	Enabled           bool                `json:"enabled"`           // Enabled 表示凭据是否允许签发任务使用。
	LastTestStatus    *string             `json:"lastTestStatus"`    // LastTestStatus 是最近一次连通性测试状态。
	LastTestMessage   *string             `json:"lastTestMessage"`   // LastTestMessage 是最近一次测试结果说明。
	LastTestAt        *time.Time          `json:"lastTestAt"`        // LastTestAt 是最近一次测试时间。
	CreatedAt         time.Time           `json:"createdAt"`         // CreatedAt 是凭据创建时间。
	UpdatedAt         time.Time           `json:"updatedAt"`         // UpdatedAt 是凭据更新时间。
}

// Order 描述一次 ACME 证书签发任务及其执行阶段。
type Order struct {
	ID            int64      `json:"id"`            // ID 是订单的数据库主键。
	SeriesID      string     `json:"seriesId"`      // SeriesID 是订单所属证书系列的稳定标识。
	SeriesCode    string     `json:"seriesCode"`    // SeriesCode 是证书系列的人类可读编码。
	SeriesName    string     `json:"seriesName"`    // SeriesName 是证书系列的显示名称。
	Domains       []string   `json:"domains"`       // Domains 是申请证书覆盖的域名列表。
	Email         string     `json:"email"`         // Email 是 ACME 账户联系邮箱。
	CAEnvironment string     `json:"caEnvironment"` // CAEnvironment 是 CA 环境标识。
	CADirectory   string     `json:"-"`             // CADirectory 是 ACME Directory 地址。
	CredentialID  int64      `json:"credentialId"`  // CredentialID 是 DNS 凭据主键。
	Status        string     `json:"status"`        // Status 是订单总体状态。
	Stage         string     `json:"stage"`         // Stage 是当前执行阶段。
	ErrorMessage  *string    `json:"errorMessage"`  // ErrorMessage 是失败原因。
	CertificateID *int64     `json:"certificateId"` // CertificateID 是成功签发后的证书主键。
	RequestedBy   *int64     `json:"-"`             // RequestedBy 是发起订单的用户主键。
	StartedAt     *time.Time `json:"startedAt"`     // StartedAt 是任务开始执行时间。
	CompletedAt   *time.Time `json:"completedAt"`   // CompletedAt 是任务完成时间。
	CreatedAt     time.Time  `json:"createdAt"`     // CreatedAt 是订单创建时间。
	UpdatedAt     time.Time  `json:"updatedAt"`     // UpdatedAt 是订单更新时间。
}

// CertificateSeries 描述一组固定域名组合及其连续签发版本。
type CertificateSeries struct {
	ID                   string     `json:"id"`                   // ID 是系列不可变的稳定标识。
	Code                 string     `json:"code"`                 // Code 是便于人工识别和沟通的唯一编码。
	Name                 string     `json:"name"`                 // Name 是系列显示名称。
	Domains              []string   `json:"domains"`              // Domains 是该系列不可变的域名组合。
	DomainsHash          string     `json:"-"`                    // DomainsHash 是顺序无关的域名集合摘要。
	Email                string     `json:"email"`                // Email 是自动续期使用的 ACME 联系邮箱。
	CAEnvironment        string     `json:"caEnvironment"`        // CAEnvironment 是生产或测试环境标识。
	CADirectory          string     `json:"-"`                    // CADirectory 是系列绑定的 ACME Directory。
	CredentialID         int64      `json:"credentialId"`         // CredentialID 是自动续期使用的 DNS 凭据主键。
	CurrentCertificateID *int64     `json:"currentCertificateId"` // CurrentCertificateID 是当前已签发版本的证书主键。
	CurrentVersion       int        `json:"currentVersion"`       // CurrentVersion 是当前证书版本号。
	CurrentStatus        *string    `json:"currentStatus"`        // CurrentStatus 是当前证书生命周期状态。
	CurrentNotAfter      *time.Time `json:"currentNotAfter"`      // CurrentNotAfter 是当前证书到期时间。
	AutoRenew            bool       `json:"autoRenew"`            // AutoRenew 表示是否自动创建续期订单。
	Enabled              bool       `json:"enabled"`              // Enabled 表示系列是否允许签发和部署。
	Revision             int64      `json:"revision"`             // Revision 是系列配置修订号。
	CreatedAt            time.Time  `json:"createdAt"`            // CreatedAt 是系列创建时间。
	UpdatedAt            time.Time  `json:"updatedAt"`            // UpdatedAt 是系列更新时间。
}

// Certificate 描述已签发证书的元数据及磁盘存储位置。
type Certificate struct {
	ID           int64     `json:"id"`           // ID 是证书的数据库主键。
	SeriesID     string    `json:"seriesId"`     // SeriesID 是证书所属系列的稳定标识。
	SeriesCode   string    `json:"seriesCode"`   // SeriesCode 是证书所属系列的可读编码。
	CommonName   string    `json:"commonName"`   // CommonName 是证书主题通用名称。
	SANs         []string  `json:"sans"`         // SANs 是证书覆盖的所有域名。
	Status       string    `json:"status"`       // Status 是证书当前生命周期状态。
	Issuer       string    `json:"issuer"`       // Issuer 是证书签发者名称。
	SerialNumber string    `json:"serialNumber"` // SerialNumber 是证书序列号。
	NotBefore    time.Time `json:"notBefore"`    // NotBefore 是证书生效时间。
	NotAfter     time.Time `json:"notAfter"`     // NotAfter 是证书过期时间。
	Fingerprint  string    `json:"fingerprint"`  // Fingerprint 是证书 SHA-256 指纹。
	Version      int       `json:"version"`      // Version 是同一证书系列内的递增版本。
	CertPath     string    `json:"-"`            // CertPath 是叶证书文件路径。
	ChainPath    string    `json:"-"`            // ChainPath 是完整证书链文件路径。
	KeyPath      string    `json:"-"`            // KeyPath 是私钥密文文件路径。
	Email        string    `json:"-"`            // Email 是对应 ACME 账户邮箱。
	CADirectory  string    `json:"-"`            // CADirectory 是签发所用 ACME Directory。
	CredentialID int64     `json:"-"`            // CredentialID 是签发所用 DNS 凭据主键。
	CreatedAt    time.Time `json:"createdAt"`    // CreatedAt 是证书记录创建时间。
	UpdatedAt    time.Time `json:"updatedAt"`    // UpdatedAt 是证书记录更新时间。
}

// Agent 描述已注册部署节点的身份、在线状态与版本信息。
type Agent struct {
	ID                   string     `json:"id"`                   // ID 是 Agent 的稳定身份标识。
	Name                 string     `json:"name"`                 // Name 是 Agent 的可读名称。
	Status               string     `json:"status"`               // Status 是按心跳计算的在线状态。
	Enabled              bool       `json:"enabled"`              // Enabled 表示控制平面是否允许该 Agent 工作。
	CertificateSerial    string     `json:"-"`                    // CertificateSerial 是当前登记的客户端证书序列号。
	CertificateExpiresAt time.Time  `json:"certificateExpiresAt"` // CertificateExpiresAt 是客户端证书过期时间。
	LastSeenAt           *time.Time `json:"lastSeenAt"`           // LastSeenAt 是最近一次认证请求时间。
	Version              string     `json:"version"`              // Version 是 Agent 程序版本。
	Platform             string     `json:"platform"`             // Platform 是 Agent 操作系统与架构。
	TargetCount          int        `json:"targetCount"`          // TargetCount 是分配给 Agent 的目标数量。
	CreatedAt            time.Time  `json:"createdAt"`            // CreatedAt 是 Agent 注册时间。
	UpdatedAt            time.Time  `json:"updatedAt"`            // UpdatedAt 是 Agent 信息更新时间。
	AuthenticatedSerial  string     `json:"-"`                    // AuthenticatedSerial 是当前请求实际使用的证书序列号。
}

// DeploymentTarget 描述证书文件需要同步到的本机或远程目标。
type DeploymentTarget struct {
	ManualSync          bool       `json:"manualSync"`          // ManualSync 表示待完成的单次远程同步，独立于自动部署开关。
	ID                  int64      `json:"id"`                  // ID 是部署目标的数据库主键。
	ExecutorType        string     `json:"executorType"`        // ExecutorType 是本机或 Agent 执行类型。
	AgentID             *string    `json:"agentId"`             // AgentID 是远程执行时绑定的 Agent 标识。
	ExecutorName        string     `json:"executorName"`        // ExecutorName 是界面展示的执行器名称。
	Name                string     `json:"name"`                // Name 是部署目标名称。
	CertificateSeriesID *string    `json:"certificateSeriesId"` // CertificateSeriesID 是目标绑定的证书系列标识。
	SeriesCode          string     `json:"seriesCode"`          // SeriesCode 是绑定系列的可读编码。
	SeriesName          string     `json:"seriesName"`          // SeriesName 是绑定系列的显示名称。
	SeriesDomains       []string   `json:"seriesDomains"`       // SeriesDomains 是绑定系列覆盖的完整域名组合。
	CommonName          string     `json:"commonName"`          // CommonName 是匹配证书的主域名。
	TargetAlias         string     `json:"targetAlias"`         // TargetAlias 是部署目录使用的安全别名。
	RuntimePath         string     `json:"runtimePath"`         // RuntimePath 是业务程序读取的稳定 current 路径。
	Enabled             bool       `json:"enabled"`             // Enabled 表示是否同步新证书。
	Revision            int64      `json:"revision"`            // Revision 是期望配置的递增修订号。
	Status              string     `json:"status"`              // Status 是最近一次部署状态。
	LastCertificateID   *int64     `json:"lastCertificateId"`   // LastCertificateID 是最近部署的证书主键。
	LastFingerprint     *string    `json:"lastFingerprint"`     // LastFingerprint 是最近部署的证书指纹。
	LastDeployedAt      *time.Time `json:"lastDeployedAt"`      // LastDeployedAt 是最近一次成功部署时间。
	LastError           *string    `json:"lastError"`           // LastError 是最近一次部署失败原因。
	CreatedAt           time.Time  `json:"createdAt"`           // CreatedAt 是目标创建时间。
	UpdatedAt           time.Time  `json:"updatedAt"`           // UpdatedAt 是目标更新时间。
}

// DeploymentRecord 描述一次目标证书切换的审计结果。
type DeploymentRecord struct {
	ID            int64     `json:"id"`            // ID 是部署记录的数据库主键。
	TargetID      *int64    `json:"targetId"`      // TargetID 是关联部署目标主键。
	ExecutorType  string    `json:"executorType"`  // ExecutorType 是本次执行器类型。
	AgentID       *string   `json:"agentId"`       // AgentID 是实际执行的 Agent 标识。
	CertificateID *int64    `json:"certificateId"` // CertificateID 是本次部署证书主键。
	Fingerprint   *string   `json:"fingerprint"`   // Fingerprint 是本次部署证书指纹。
	Status        string    `json:"status"`        // Status 是本次部署结果状态。
	Message       string    `json:"message"`       // Message 是部署结果说明。
	CreatedAt     time.Time `json:"createdAt"`     // CreatedAt 是记录创建时间。
}

// AuditLog 描述管理员敏感操作的不可变审计记录。
type AuditLog struct {
	ID           int64     `json:"id"`           // ID 是审计记录主键。
	UserID       *int64    `json:"userId"`       // UserID 是发起操作的用户主键，系统任务可为空。
	Username     string    `json:"username"`     // Username 是操作发生时可查询到的用户名。
	Action       string    `json:"action"`       // Action 是 CREATE、UPDATE、DELETE 等动作。
	ResourceType string    `json:"resourceType"` // ResourceType 是被操作资源类型。
	ResourceID   string    `json:"resourceId"`   // ResourceID 是被操作资源的稳定标识。
	Detail       string    `json:"detail"`       // Detail 是不包含秘密的操作摘要。
	CreatedAt    time.Time `json:"createdAt"`    // CreatedAt 是操作发生时间。
}

// AuditLogPage 描述使用游标分页返回的审计记录集合。
type AuditLogPage struct {
	Items      []AuditLog `json:"items"`      // Items 是当前页审计记录。
	NextCursor *int64     `json:"nextCursor"` // NextCursor 是下一页起点；为空表示没有更多记录。
}

// Dashboard 汇总运行概览页面所需的核心指标。
type Dashboard struct {
	HistoryVersionTotal    int64         `json:"historyVersionTotal"`    // HistoryVersionTotal 是包含历史版本的证书库总数。
	UpcomingCertificates   []Certificate `json:"upcomingCertificates"`   // UpcomingCertificates 是全库在用版本中最早到期的五份证书。
	CertificateTotal       int64         `json:"certificateTotal"`       // CertificateTotal 是证书总数。
	CertificateValid       int64         `json:"certificateValid"`       // CertificateValid 是有效证书数。
	CertificateExpiring    int64         `json:"certificateExpiring"`    // CertificateExpiring 是即将过期证书数。
	CertificateExpired     int64         `json:"certificateExpired"`     // CertificateExpired 是已过期证书数。
	OrderRunning           int64         `json:"orderRunning"`           // OrderRunning 是运行中订单数。
	OrderFailed            int64         `json:"orderFailed"`            // OrderFailed 是失败订单数。
	AgentOnline            int64         `json:"agentOnline"`            // AgentOnline 是在线 Agent 数。
	AgentTotal             int64         `json:"agentTotal"`             // AgentTotal 是 Agent 总数。
	NearestExpiry          *time.Time    `json:"nearestExpiry"`          // NearestExpiry 是最近的证书过期时间。
	Role                   string        `json:"role"`                   // Role 是当前节点运行角色。
	LocalDeploymentEnabled bool          `json:"localDeploymentEnabled"` // LocalDeploymentEnabled 表示本机部署能力是否启用。
}

// ACMEAccount 描述可复用的 ACME 账户私钥与注册信息。
type ACMEAccount struct {
	Key              string  // Key 是邮箱与目录组合出的账户唯一键。
	Email            string  // Email 是账户联系邮箱。
	CADirectory      string  // CADirectory 是账户所属 ACME Directory。
	PrivateKey       string  // PrivateKey 是账户私钥 PEM。
	RegistrationJSON *string // RegistrationJSON 是 ACME 服务端返回的注册信息。
}

// DesiredTarget 描述 Agent 下一次轮询应达到的部署状态。
type DesiredTarget struct {
	ManualSync  bool                   `json:"manualSync,omitempty"` // ManualSync 要求 Agent 上报执行中状态，修订号变化保证重新物化。
	ID          int64                  `json:"id"`                   // ID 是部署目标主键。
	SeriesID    string                 `json:"seriesId"`             // SeriesID 是目标绑定的证书系列标识。
	Name        string                 `json:"name"`                 // Name 是目标名称。
	TargetAlias string                 `json:"targetAlias"`          // TargetAlias 是本地目录别名。
	CommonName  string                 `json:"commonName"`           // CommonName 是目标匹配的证书主域名。
	Domains     []string               `json:"domains"`              // Domains 是目标系列覆盖的完整域名集合。
	Revision    int64                  `json:"revision"`             // Revision 是期望配置修订号。
	Certificate *CertificateDescriptor `json:"certificate"`          // Certificate 是应部署的证书描述；无可用证书时为空。
}

// CertificateDescriptor 描述 Agent 下载证书包所需的最小元数据。
type CertificateDescriptor struct {
	ID          int64     `json:"id"`          // ID 是证书主键。
	SeriesID    string    `json:"seriesId"`    // SeriesID 是证书所属系列标识。
	Version     int       `json:"version"`     // Version 是证书在系列内的版本号。
	Fingerprint string    `json:"fingerprint"` // Fingerprint 是证书 SHA-256 指纹。
	NotAfter    time.Time `json:"notAfter"`    // NotAfter 是证书过期时间。
	DownloadURL string    `json:"downloadUrl"` // DownloadURL 是需要双向 TLS 认证的下载路径。
}
