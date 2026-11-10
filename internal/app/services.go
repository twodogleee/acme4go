// Package app 编排控制平面的领域服务、后台任务和外部适配器。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/twodogleee/acme4go/internal/acmeissuer"
	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/twodogleee/acme4go/internal/cloud/aliyun"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/pki"
	"github.com/twodogleee/acme4go/internal/store"
)

// Error 是 HTTP 和后台任务共享的安全业务错误，不向调用方泄漏底层敏感上下文。
type Error struct {
	Status  int    // Status 是映射到 HTTP 响应的状态码。
	Code    string // Code 是供客户端稳定判断的业务错误码。
	Message string // Message 是可以安全展示给最终用户的错误说明。
}

// Error 返回业务错误的安全说明文本。
func (e *Error) Error() string { return e.Message }

// BadRequest 创建参数或请求语义错误。
func BadRequest(message string) error {
	return &Error{Status: 400, Code: "BAD_REQUEST", Message: message}
}

// Unauthorized 创建缺少有效登录身份的错误。
func Unauthorized(message string) error {
	return &Error{Status: 401, Code: "UNAUTHORIZED", Message: message}
}

// Forbidden 创建身份有效但无权执行操作的错误。
func Forbidden(message string) error { return &Error{Status: 403, Code: "FORBIDDEN", Message: message} }

// NotFound 创建目标资源不存在的错误。
func NotFound(message string) error { return &Error{Status: 404, Code: "NOT_FOUND", Message: message} }

// Conflict 创建请求与资源当前状态冲突的错误。
func Conflict(message string) error { return &Error{Status: 409, Code: "CONFLICT", Message: message} }

// Services 聚合 Server 角色共享服务；它不在 Agent 角色中初始化。
type Services struct {
	Cloud       *cloud.Registry    // Cloud 是独立云证书与产品适配器注册表。
	cloudWake   chan struct{}      // cloudWake 合并云任务和新证书的唤醒通知。
	Config      config.Config      // Config 是校验后的 Server 运行配置。
	Store       *store.Store       // Store 是控制平面持久化仓库。
	Vault       *cryptoutil.Vault  // Vault 负责敏感材料加解密。
	Storage     *deploy.Storage    // Storage 负责证书库的原子文件写入。
	Issuer      *acmeissuer.Issuer // Issuer 负责 ACME DNS-01 签发。
	PKI         *pki.Manager       // PKI 负责内部 CA 与 Agent 身份证书。
	logger      *slog.Logger       // logger 记录服务端结构化运行日志。
	orderSlots  chan struct{}      // orderSlots 限制并发签发任务数量。
	orderWG     sync.WaitGroup     // orderWG 跟踪签发任务和调度协程。
	serviceCtx  context.Context    // serviceCtx 是全部后台工作的生命周期上下文。
	serviceStop context.CancelFunc // serviceStop 取消全部后台工作。
	lifecycleMu sync.Mutex         // lifecycleMu 保护启动和关闭状态。
	closing     bool               // closing 表示服务已经进入关闭流程。
	background  bool               // background 表示调度器已经启动。
	closeOnce   sync.Once          // closeOnce 保证资源只关闭一次。
	closeErr    error              // closeErr 保存首次关闭数据库的结果。
}

// NewServices 初始化 Server 角色服务，不启动监听器。
func NewServices(cfg config.Config, logger *slog.Logger) (*Services, error) {
	vault, err := cryptoutil.NewVault(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	database, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	pkiManager, err := pki.LoadOrCreate(cfg.DataDir, cfg.AgentPublicURL, vault)
	if err != nil {
		database.Close()
		return nil, err
	}
	serviceCtx, serviceStop := context.WithCancel(context.Background())
	services := &Services{
		Cloud:       aliyun.NewRegistry(),
		cloudWake:   make(chan struct{}, 1),
		Config:      cfg,
		Store:       database,
		Vault:       vault,
		Storage:     deploy.NewStorage(cfg.DataDir, vault),
		Issuer:      acmeissuer.New(database, vault, cfg.DNSPropagationTimeout, cfg.DNSPollingInterval),
		PKI:         pkiManager,
		logger:      logger,
		orderSlots:  make(chan struct{}, 2),
		serviceCtx:  serviceCtx,
		serviceStop: serviceStop,
	}
	if err := services.initialize(context.Background()); err != nil {
		serviceStop()
		database.Close()
		return nil, err
	}
	return services, nil
}

// initialize 恢复异常中断状态、初始化管理员并准备本机部署目录。
func (s *Services) initialize(ctx context.Context) error {
	if err := s.Store.FailInterruptedOrders(ctx, s.Config.AlertWebhookURL != ""); err != nil {
		return err
	}
	count, err := s.Store.UserCount(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		if strings.TrimSpace(s.Config.AdminPassword) == "" {
			return errors.New("首次启动必须配置 ACME_ADMIN_PASSWORD 或 ACME_ADMIN_PASSWORD_FILE")
		}
		hash, err := cryptoutil.HashPassword(s.Config.AdminPassword)
		if err != nil {
			return err
		}
		if _, err := s.Store.CreateUser(ctx, s.Config.AdminUsername, hash, "系统管理员"); err != nil {
			return err
		}
		s.logger.Info("已创建初始管理员", "username", s.Config.AdminUsername)
	}
	if s.Config.LocalDeploymentEnabled {
		return os.MkdirAll(s.Config.DeploymentDir, 0o750)
	}
	return nil
}

// StartBackground 启动续期、历史清理、本机部署对账和独立通知任务。
func (s *Services) StartBackground(ctx context.Context) {
	s.lifecycleMu.Lock()
	if s.closing || s.background {
		s.lifecycleMu.Unlock()
		return
	}
	s.background = true
	s.orderWG.Add(3)
	s.lifecycleMu.Unlock()
	stopParentCancellation := context.AfterFunc(ctx, s.serviceStop)
	// 云部署使用独立工作者，外部 API 延迟不会阻塞续期和文件部署。
	go func() {
		defer s.orderWG.Done()
		s.runCloudWorker(s.serviceCtx)
	}()
	// 通知工作者使用独立周期，避免被默认一小时的续期扫描延迟。
	go func() {
		defer s.orderWG.Done()
		s.runNotifications(s.serviceCtx)
	}()
	// 调度协程在父上下文结束或服务关闭时统一退出。
	go func() {
		defer s.orderWG.Done()
		defer stopParentCancellation()
		s.runScheduler(s.serviceCtx)
	}()
}

// Close 先取消所有后台工作并等待退出，再关闭共享数据库连接。
func (s *Services) Close() error {
	// 首次关闭回调取消后台任务、等待完成并关闭数据库。
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closing = true
		s.serviceStop()
		s.lifecycleMu.Unlock()
		s.orderWG.Wait()
		s.closeErr = s.Store.Close()
	})
	return s.closeErr
}

// runScheduler 按独立周期执行日常维护和 Agent 服务端证书轮换。
func (s *Services) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(s.Config.SchedulerInterval)
	defer ticker.Stop()
	tlsTicker := time.NewTicker(12 * time.Hour)
	defer tlsTicker.Stop()
	s.rotateAgentServerCertificate()
	s.runMaintenance(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runMaintenance(ctx)
		case <-tlsTicker.C:
			s.rotateAgentServerCertificate()
		}
	}
}

// rotateAgentServerCertificate 在固定的独立周期内轮换 Agent HTTPS 服务端证书。
func (s *Services) rotateAgentServerCertificate() {
	expiresAt, rotated, err := s.PKI.RotateServerCertificateIfNeeded(time.Now().UTC(), 30*24*time.Hour)
	if err != nil {
		s.logger.Error("轮换 Agent TLS 服务端证书失败", "error", err)
		return
	}
	if rotated {
		s.logger.Info("Agent TLS 服务端证书已轮换", "expires_at", expiresAt)
	}
}

// runMaintenance 清理过期数据、创建续期订单并对账本机部署目标。
func (s *Services) runMaintenance(ctx context.Context) {
	if err := s.Store.DeleteExpiredSessions(ctx, time.Now()); err != nil {
		s.logger.Error("清理过期会话失败", "error", err)
	}
	if err := s.Store.DeleteExpiredEnrollmentTokens(ctx, time.Now()); err != nil {
		s.logger.Error("清理 Agent 注册令牌失败", "error", err)
	}
	historyRetention := s.Config.HistoryRetention
	if historyRetention <= 0 {
		historyRetention = 730 * 24 * time.Hour
	}
	retentionBefore := time.Now().Add(-historyRetention)
	if err := s.Store.PruneAlerts(ctx, retentionBefore); err != nil {
		s.logger.Error("清理告警历史失败", "error", err)
	}
	if err := s.Store.PruneDeploymentRecords(ctx, retentionBefore, 100000); err != nil {
		s.logger.Error("清理部署历史记录失败", "error", err)
	}
	if err := s.Store.PruneOrders(ctx, retentionBefore, 100000); err != nil {
		s.logger.Error("清理终态证书订单失败", "error", err)
	}
	if err := s.Store.PruneAuditLogs(ctx, retentionBefore, 100000); err != nil {
		s.logger.Error("清理审计日志失败", "error", err)
	}
	if s.Config.LocalDeploymentEnabled {
		var removed int
		// 历史目录清理与本机文件切换共享协调权，不能删除正在恢复的版本。
		err := s.Store.WithLocalDeployment(ctx, func() error {
			var err error
			removed, err = deploy.PruneMaterializedVersions(s.Config.DeploymentDir, retentionBefore, 3)
			return err
		})
		if err != nil {
			s.logger.Error("清理本机部署历史版本失败", "error", err)
		} else if removed > 0 {
			s.logger.Info("已清理本机部署历史版本", "count", removed)
		}
	}
	seriesValues, err := s.Store.CertificateSeriesDueForRenewal(ctx, time.Now().Add(s.Config.RenewBefore), s.Config.RenewBefore)
	if err != nil {
		s.logger.Error("扫描待续期证书失败", "error", err)
		return
	}
	for _, series := range seriesValues {
		active, err := s.Store.HasActiveSeriesOrder(ctx, series.ID, time.Now().Add(-12*time.Hour))
		if err != nil || active {
			continue
		}
		environment := "LETS_ENCRYPT_PRODUCTION"
		if series.CADirectory == store.StagingDirectory {
			environment = "LETS_ENCRYPT_STAGING"
		}
		order, err := s.Store.CreateOrder(ctx, store.Order{
			SeriesID: series.ID, SeriesCode: series.Code, SeriesName: series.Name, Domains: series.Domains, Email: series.Email, CAEnvironment: environment,
			CADirectory: series.CADirectory, CredentialID: series.CredentialID,
		})
		if err != nil {
			if errors.Is(err, store.ErrActiveOrder) {
				continue
			}
			s.logger.Error("创建自动续期订单失败", "series_id", series.ID, "error", err)
			s.PublishAlert(Alert{Event: "certificate.renewal.schedule_failed", Severity: "ERROR", ResourceType: "CERTIFICATE_SERIES", ResourceID: series.ID, Message: "无法创建自动续期订单"})
			continue
		}
		s.RunOrder(order.ID)
	}
	if s.Config.LocalDeploymentEnabled {
		if err := s.ReconcileLocalTargets(ctx); err != nil {
			s.logger.Error("本机部署对账失败", "error", err)
		}
	}
}

// internalError 记录完整内部错误，并向上层返回不泄密的统一错误。
func (s *Services) internalError(operation string, err error) error {
	s.logger.Error(operation, "error", err)
	return &Error{Status: 500, Code: "INTERNAL_ERROR", Message: "服务器无法完成请求"}
}

// RecordAudit 持久化不可变审计事件，并确保写入失败不会静默丢失。
func (s *Services) RecordAudit(ctx context.Context, userID *int64, action, resourceType, resourceID, detail string) {
	if err := s.Store.Audit(ctx, userID, action, resourceType, resourceID, detail); err != nil {
		s.logger.Error("写入审计日志失败", "action", action, "resource_type", resourceType, "resource_id", resourceID, "error", err)
	}
}

// normalizeStoreError 将仓库的未找到错误转换为安全业务错误。
func normalizeStoreError(err error, notFoundMessage string) error {
	if errors.Is(err, store.ErrNotFound) {
		return NotFound(notFoundMessage)
	}
	return err
}

// validAlias 校验仅含小写字母、数字、连字符和下划线的目录别名。
func validAlias(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '-' || char == '_') {
			continue
		}
		return false
	}
	return true
}

// required 去除输入空白并为必填字段生成统一错误。
func required(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", BadRequest(fmt.Sprintf("%s不能为空", field))
	}
	return value, nil
}

// formatID 将数据库整数主键转换为审计日志使用的十进制文本。
func formatID(value int64) string { return strconv.FormatInt(value, 10) }
