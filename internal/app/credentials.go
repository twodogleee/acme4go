// 本文件统一管理 DNS 与云部署共享的访问密钥，按用途校验并保留历史引用。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"strings"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/store"
)

// CredentialInput 在服务商凭据基础上显式选择可用用途。
type CredentialInput struct {
	DNSCredentialInput      // DNSCredentialInput 保持名称、区域及秘密字段契约。
	DNSEnabled         bool `json:"dnsEnabled"`   // DNSEnabled 允许用于 DNS-01 验证。
	CloudEnabled       bool `json:"cloudEnabled"` // CloudEnabled 允许用于阿里云证书及产品部署。
}

// ListCredentials 返回统一脱敏列表，不暴露密文和加密上下文。
func (s *Services) ListCredentials(ctx context.Context) ([]store.DNSCredential, error) {
	values, err := s.Store.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	for i := range values {
		if err := s.decryptDNS(&values[i]); err != nil {
			return nil, s.internalError("解密共享凭据失败", err)
		}
		values[i].MaskedAccessKeyID = maskedDNSIdentifier(values[i].Provider, values[i].AccessKeyID)
		values[i].AccessKeyID, values[i].AccessKeySecret = "", ""
	}
	return values, nil
}

// SaveCredential 保存一份秘密供多个用途引用；已有云身份不能通过替换 AccessKeyId 改变。
func (s *Services) SaveCredential(ctx context.Context, userID, id int64, input CredentialInput) (store.DNSCredential, error) {
	if !input.DNSEnabled && !input.CloudEnabled {
		return store.DNSCredential{}, BadRequest("至少选择一种凭据用途")
	}
	if input.CloudEnabled && input.Provider != "ALIYUN" {
		return store.DNSCredential{}, BadRequest("当前仅阿里云支持云部署用途")
	}
	if len([]rune(input.Name)) > 100 || len(input.AccessKeyID) > 256 || len(input.AccessKeySecret) > 1024 {
		return store.DNSCredential{}, BadRequest("凭据名称或密钥长度无效")
	}
	var existing *store.DNSCredential
	if id != 0 {
		old, err := s.Store.FindCredential(ctx, id)
		if err != nil {
			return old, normalizeStoreError(err, "凭据不存在")
		}
		if err := s.decryptDNS(&old); err != nil {
			return old, s.internalError("解密共享凭据失败", err)
		}
		if old.AccessKeyIDLocked && strings.TrimSpace(input.AccessKeyID) != "" && strings.TrimSpace(input.AccessKeyID) != old.AccessKeyID {
			return store.DNSCredential{}, BadRequest("已启用过云部署用途的 AccessKeyId 不可替换，请新增凭据")
		}
		existing = &old
	}
	value, err := s.prepareProviderInput(input.DNSCredentialInput, existing, input.DNSEnabled)
	if err != nil {
		return value, err
	}
	value.ID, value.CloudEnabled = id, input.CloudEnabled
	key := strings.TrimSpace(input.AccessKeyID)
	if key == "" && existing != nil {
		key = existing.AccessKeyID
	}
	value.ID, err = s.Store.SaveCredential(ctx, value, cryptoutil.TokenHash(key))
	if err != nil {
		var scopeError *store.CredentialScopeError
		if errors.As(err, &scopeError) {
			return store.DNSCredential{}, Conflict(scopeError.Error())
		}
		return store.DNSCredential{}, Conflict("凭据正在执行云任务或相同 AccessKey 已关联云凭据，请在现有凭据上调整用途")
	}
	s.RecordAudit(ctx, &userID, "SAVE", "CREDENTIAL", formatID(value.ID), "保存共享凭据及用途")
	value, err = s.Store.FindCredential(ctx, value.ID)
	value.MaskedAccessKeyID = maskedDNSIdentifier(value.Provider, key)
	value.AccessKeyID, value.AccessKeySecret = "", ""
	return value, err
}

// DeleteCredential 依靠引用约束保护证书系列、订单、云规则及上传记录。
func (s *Services) DeleteCredential(ctx context.Context, userID, id int64) error {
	if err := s.Store.DeleteCredential(ctx, id); err != nil {
		return Conflict("凭据已被证书或云资源引用，不能删除；可停用凭据")
	}
	s.RecordAudit(ctx, &userID, "DELETE", "CREDENTIAL", formatID(id), "删除未使用凭据")
	return nil
}
