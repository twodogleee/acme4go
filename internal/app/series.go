// 本文件实现证书系列续期配置的显式维护，避免签发与重试隐式覆盖长期设置。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/twodogleee/acme4go/internal/store"
)

// CertificateSeriesInput 描述可独立修改的证书系列长期配置。
type CertificateSeriesInput struct {
	Name         string `json:"name"`         // Name 是系列显示名称。
	Email        string `json:"email"`        // Email 是后续签发和自动续期联系邮箱。
	CredentialID int64  `json:"credentialId"` // CredentialID 是后续签发使用的 DNS 凭据。
	AutoRenew    bool   `json:"autoRenew"`    // AutoRenew 表示是否自动创建续期订单。
	Enabled      bool   `json:"enabled"`      // Enabled 表示系列是否允许继续签发和部署。
}

// UpdateCertificateSeries 校验并显式保存系列长期配置。
func (s *Services) UpdateCertificateSeries(ctx context.Context, userID int64, id string, input CertificateSeriesInput) (store.CertificateSeries, error) {
	name := strings.TrimSpace(input.Name)
	email := strings.TrimSpace(input.Email)
	if name == "" || len([]rune(name)) > 100 {
		return store.CertificateSeries{}, BadRequest("证书系列名称不能为空且不能超过 100 个字符")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return store.CertificateSeries{}, BadRequest("联系邮箱格式无效")
	}
	existing, err := s.Store.FindCertificateSeries(ctx, id, s.Config.RenewBefore)
	if err != nil {
		return store.CertificateSeries{}, normalizeStoreError(err, "证书系列不存在")
	}
	credential, err := s.Store.FindDNSCredential(ctx, input.CredentialID)
	if err != nil {
		return store.CertificateSeries{}, normalizeStoreError(err, "DNS 凭据不存在")
	}
	if (input.Enabled || input.AutoRenew) && !credential.Enabled {
		return store.CertificateSeries{}, BadRequest("启用系列或自动续期时必须选择已启用的 DNS 凭据")
	}
	if err := validateDomainsInZone(existing.Domains, credential.Zones...); err != nil {
		return store.CertificateSeries{}, err
	}
	existing.Name = name
	existing.Email = email
	existing.CredentialID = input.CredentialID
	existing.AutoRenew = input.AutoRenew
	existing.Enabled = input.Enabled
	if err := s.Store.UpdateCertificateSeries(ctx, existing, credential.Zones...); err != nil {
		if errors.Is(err, store.ErrSeriesChanged) {
			return store.CertificateSeries{}, Conflict("凭据配置已变化，请刷新后重试")
		}
		return store.CertificateSeries{}, s.internalError("更新证书系列失败", err)
	}
	s.RecordAudit(ctx, &userID, "UPDATE", "CERTIFICATE_SERIES", id, existing.Code+" · 更新续期配置")
	return s.Store.FindCertificateSeries(ctx, id, s.Config.RenewBefore)
}
