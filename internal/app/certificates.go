// 本文件编排证书订单、ACME 签发、续期和加密证书库。
//
// @author Andersen
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/dnsnames"
	"github.com/twodogleee/acme4go/internal/store"
)

// CreateOrderInput 是管理员提交的新证书申请参数。
type CreateOrderInput struct {
	Name          string   `json:"name"`          // Name 是证书系列的可读名称。
	Domains       []string `json:"domains"`       // Domains 是证书需要覆盖的域名列表。
	Email         string   `json:"email"`         // Email 是 ACME 账户联系邮箱。
	CAEnvironment string   `json:"caEnvironment"` // CAEnvironment 是生产或测试环境标识。
	CredentialID  int64    `json:"credentialId"`  // CredentialID 是 DNS-01 使用的凭据主键。
	AgreeTerms    bool     `json:"agreeTerms"`    // AgreeTerms 表示申请人是否接受 CA 服务条款。
}

// CreateOrder 校验申请参数、创建持久化订单并异步启动签发。
func (s *Services) CreateOrder(ctx context.Context, userID *int64, input CreateOrderInput) (store.Order, error) {
	domains, err := normalizeDomains(input.Domains)
	if err != nil {
		return store.Order{}, err
	}
	if _, err := mail.ParseAddress(strings.TrimSpace(input.Email)); err != nil {
		return store.Order{}, BadRequest("联系邮箱格式无效")
	}
	if !input.AgreeTerms {
		return store.Order{}, BadRequest("必须同意 ACME 服务条款")
	}
	directory, err := store.CAEnvironmentDirectory(input.CAEnvironment)
	if err != nil {
		return store.Order{}, BadRequest("不支持的 CA 环境")
	}
	seriesName := strings.TrimSpace(input.Name)
	if seriesName == "" {
		seriesName = domains[0]
	}
	if len([]rune(seriesName)) > 100 {
		return store.Order{}, BadRequest("证书系列名称不能超过 100 个字符")
	}
	series, err := s.Store.FindCertificateSeriesByIdentity(ctx, directory, store.DomainSetHash(domains), s.Config.RenewBefore)
	if errors.Is(err, store.ErrNotFound) {
		seriesID, seriesCode, identityErr := newSeriesIdentity()
		if identityErr != nil {
			return store.Order{}, s.internalError("生成证书系列标识失败", identityErr)
		}
		series = store.CertificateSeries{ID: seriesID, Code: seriesCode, Name: seriesName, Domains: domains,
			DomainsHash: store.DomainSetHash(domains), Email: strings.TrimSpace(input.Email),
			CAEnvironment: input.CAEnvironment, CADirectory: directory, CredentialID: input.CredentialID, Enabled: true}
		err = nil
	}
	if err != nil {
		return store.Order{}, s.internalError("读取证书系列失败", err)
	}
	if !series.Enabled {
		return store.Order{}, Conflict("证书系列已停用，请先在系列配置中启用")
	}
	credential, err := s.DecryptedDNSCredential(ctx, series.CredentialID)
	if err != nil {
		return store.Order{}, err
	}
	if err := validateDomainsInZone(series.Domains, credential.Zones...); err != nil {
		return store.Order{}, err
	}
	order, err := s.Store.CreateOrderWithSeries(ctx, series, userID, credential.Zones...)
	if errors.Is(err, store.ErrActiveOrder) || errors.Is(err, store.ErrSeriesChanged) {
		return store.Order{}, Conflict(err.Error())
	}
	if err != nil {
		return store.Order{}, s.internalError("创建证书订单失败", err)
	}
	if userID != nil {
		s.RecordAudit(ctx, userID, "CREATE", "CERTIFICATE_ORDER", formatID(order.ID), series.Code+" · "+strings.Join(series.Domains, ","))
	}
	s.RunOrder(order.ID)
	return order, nil
}

// RunOrder 在有界后台槽位中执行签发，API 请求不会被 DNS 传播等待阻塞。
func (s *Services) RunOrder(orderID int64) {
	s.lifecycleMu.Lock()
	if s.closing {
		s.lifecycleMu.Unlock()
		return
	}
	s.orderWG.Add(1)
	s.lifecycleMu.Unlock()
	// 签发协程等待有界槽位并使用服务生命周期上下文执行订单。
	go func() {
		defer s.orderWG.Done()
		select {
		case s.orderSlots <- struct{}{}:
			// 订单结束回调释放签发并发槽位。
			defer func() { <-s.orderSlots }()
		case <-s.serviceCtx.Done():
			s.failOrder(orderID, "服务停止导致排队任务中断，请重试")
			return
		}
		// 槽位与取消信号可能同时就绪，取得槽位后也必须终结已取消的排队任务。
		if s.serviceCtx.Err() != nil {
			s.failOrder(orderID, "服务停止导致排队任务中断，请重试")
			return
		}
		ctx, cancel := context.WithTimeout(s.serviceCtx, 15*time.Minute)
		defer cancel()
		s.processOrder(ctx, orderID)
	}()
}

// processOrder 执行单个订单的 DNS 验证、签发、加密保存和登记流程。
func (s *Services) processOrder(ctx context.Context, orderID int64) {
	if err := s.Store.StartOrder(ctx, orderID); err != nil {
		s.logger.Error("启动证书订单失败", "order_id", orderID, "error", err)
		if ctx.Err() != nil {
			s.failOrder(orderID, "服务停止导致任务中断，请重试")
		}
		return
	}
	order, err := s.Store.FindOrder(ctx, orderID)
	if err != nil {
		s.failOrder(orderID, "无法读取证书订单")
		return
	}
	credential, err := s.DecryptedDNSCredential(ctx, order.CredentialID)
	if err != nil {
		s.failOrder(orderID, safeOrderError(err))
		return
	}
	if err := validateDomainsInZone(order.Domains, credential.Zones...); err != nil {
		s.failOrder(orderID, "DNS 凭据根域名已变化，不再覆盖当前订单域名")
		return
	}
	// 阶段回调把 ACME 执行进度持久化到订单记录。
	bundle, err := s.Issuer.Issue(ctx, order, credential, func(stage string) {
		_ = s.Store.UpdateOrderStage(ctx, orderID, stage)
	})
	if err != nil {
		s.logger.Error("证书签发失败", "order_id", orderID, "error", err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.failOrder(orderID, "签发超过 15 分钟，请检查 DNS 传播和 CA 状态后重试")
		} else if ctx.Err() != nil {
			s.failOrder(orderID, "服务停止导致任务中断，请重试")
		} else {
			s.failOrder(orderID, "ACME 证书签发失败，请检查 DNS 凭据、根域名和订单日志")
		}
		return
	}
	if store.DomainSetHash(bundle.SANs) != store.DomainSetHash(order.Domains) {
		s.logger.Error("签发证书域名集合与系列不一致", "order_id", orderID, "series_id", order.SeriesID)
		s.failOrder(orderID, "签发证书域名与订单不一致，已拒绝入库")
		return
	}
	_ = s.Store.UpdateOrderStage(ctx, orderID, "STORING")
	certPath, chainPath, keyPath, err := s.Storage.Save(bundle)
	if err != nil {
		s.logger.Error("保存已签发证书失败", "order_id", orderID, "error", err)
		s.failOrder(orderID, "证书已签发但无法安全保存")
		return
	}
	certificate, err := s.Store.InsertCertificate(ctx, store.Certificate{
		SeriesID: order.SeriesID, CommonName: bundle.CommonName, SANs: bundle.SANs, Issuer: bundle.Issuer,
		SerialNumber: bundle.SerialNumber, NotBefore: bundle.NotBefore, NotAfter: bundle.NotAfter,
		Fingerprint: bundle.Fingerprint, CertPath: certPath, ChainPath: chainPath, KeyPath: keyPath,
		Email: order.Email, CADirectory: order.CADirectory, CredentialID: order.CredentialID,
	})
	if err != nil {
		s.logger.Error("登记已签发证书失败", "order_id", orderID, "error", err)
		s.failOrder(orderID, "证书已保存但元数据登记失败")
		return
	}
	if err := s.Store.CompleteOrder(ctx, orderID, certificate.ID); err != nil {
		s.logger.Error("完成证书订单失败", "order_id", orderID, "error", err)
		s.failOrder(orderID, "证书已保存但订单状态登记失败，请检查系统日志")
		return
	}
	if s.Config.LocalDeploymentEnabled {
		if err := s.ReconcileLocalTargets(ctx); err != nil {
			s.logger.Error("新证书本机部署失败", "certificate_id", certificate.ID, "error", err)
		}
	}
	s.logger.Info("证书签发完成", "order_id", orderID, "certificate_id", certificate.ID, "common_name", certificate.CommonName)
	s.wakeCloud()
}

// failOrder 使用独立的短超时上下文记录终态，避免关机取消后订单永久停留在运行中。
func (s *Services) failOrder(orderID int64, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notifications := s.configuredAlerts(Alert{Event: "certificate.order.failed", Severity: "ERROR", ResourceType: "CERTIFICATE_ORDER", ResourceID: formatID(orderID), Message: message})
	if err := s.Store.FailOrder(ctx, orderID, message, notifications...); err != nil {
		s.logger.Error("记录证书订单失败状态失败", "order_id", orderID, "error", err)
		return
	}
}

// RetryOrder 按失败订单的原始参数创建一笔新的签发任务。
func (s *Services) RetryOrder(ctx context.Context, userID int64, id int64) (store.Order, error) {
	existing, err := s.Store.FindOrder(ctx, id)
	if err != nil {
		return store.Order{}, normalizeStoreError(err, "证书订单不存在")
	}
	if existing.Status != "FAILED" {
		return store.Order{}, Conflict("只有失败订单可以重试")
	}
	series, err := s.Store.FindCertificateSeries(ctx, existing.SeriesID, s.Config.RenewBefore)
	if err != nil {
		return store.Order{}, normalizeStoreError(err, "证书系列不存在")
	}
	return s.CreateOrder(ctx, &userID, CreateOrderInput{Name: series.Name, Domains: series.Domains, Email: series.Email, CAEnvironment: series.CAEnvironment, CredentialID: series.CredentialID, AgreeTerms: true})
}

// CertificateBundle 读取证书元数据并解密验证对应的证书文件包。
func (s *Services) CertificateBundle(ctx context.Context, id int64) (store.Certificate, deploy.Bundle, error) {
	certificate, err := s.Store.FindCertificate(ctx, id, s.Config.RenewBefore)
	if err != nil {
		return store.Certificate{}, deploy.Bundle{}, normalizeStoreError(err, "证书不存在")
	}
	bundle, err := s.Storage.Load(certificate.CertPath, certificate.ChainPath, certificate.KeyPath, certificate.Fingerprint, certificate.CommonName)
	if err != nil {
		return store.Certificate{}, deploy.Bundle{}, s.internalError("读取证书材料失败", err)
	}
	bundle.SANs, bundle.NotAfter = certificate.SANs, certificate.NotAfter
	bundle.NotBefore, bundle.Issuer, bundle.SerialNumber = certificate.NotBefore, certificate.Issuer, certificate.SerialNumber
	return certificate, bundle, nil
}

// normalizeDomains 校验、去重并稳定排序证书域名列表。
func normalizeDomains(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 100 {
		return nil, BadRequest("证书必须包含 1 至 100 个域名")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		base := strings.TrimPrefix(value, "*.")
		if !validDNSName(base) {
			return nil, BadRequest(fmt.Sprintf("域名 %q 格式无效", value))
		}
		if strings.Contains(value, "*") && !strings.HasPrefix(value, "*.") {
			return nil, BadRequest("通配符只能出现在域名最左侧")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	// 首个域名决定证书主名称，其余名称排序以获得稳定订单表示。
	if len(result) > 2 {
		sort.Strings(result[1:])
	}
	return result, nil
}

// validDNSName 严格校验 ASCII DNS 名称总长度、标签长度和连字符位置。
func validDNSName(value string) bool { return dnsnames.ValidName(value) }

// validateDomainsInZone 确认订单每个域名至少属于一个显式配置的根域名。
func validateDomainsInZone(domains []string, zones ...string) error {
	normalized, err := dnsnames.NormalizeZones(zones)
	if err != nil || len(normalized) == 0 {
		return BadRequest("DNS 凭据根域名格式无效或未配置")
	}
	for _, domain := range domains {
		if !dnsnames.CoversDomain(normalized, domain) {
			return BadRequest(fmt.Sprintf("域名 %q 不在所选 DNS 凭据的根域名范围内（%s）", domain, strings.Join(normalized, "、")))
		}
	}
	return nil
}

// safeOrderError 仅保留允许向管理员展示的签发准备错误。
func safeOrderError(err error) string {
	var business *Error
	if errors.As(err, &business) {
		return business.Message
	}
	return "证书签发准备失败"
}

// newSeriesIdentity 生成随机 UUID 风格系列 ID 与短可读编码。
func newSeriesIdentity() (string, string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := hex.EncodeToString(bytes)
	id := fmt.Sprintf("%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:])
	return id, "CERT-" + strings.ToUpper(encoded[:16]), nil
}
