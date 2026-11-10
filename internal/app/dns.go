// 本文件实现 DNS 凭据校验、加密保存和连接测试。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/dnsnames"
	"github.com/twodogleee/acme4go/internal/dnsprovider"
	"github.com/twodogleee/acme4go/internal/store"
)

// DNSCredentialInput 是创建或修改 DNS 服务商凭据的请求参数。
type DNSCredentialInput struct {
	Name            string   `json:"name"`            // Name 是凭据的可读名称。
	Provider        string   `json:"provider"`        // Provider 是 DNS 服务商类型。
	Zone            string   `json:"zone"`            // Zone 是旧接口的单根域名；新客户端使用 Zones。
	Zones           []string `json:"zones"`           // Zones 是显式根域名列表，提供时优先于 Zone。
	AccessKeyID     string   `json:"accessKeyId"`     // AccessKeyID 是服务商访问密钥标识。
	AccessKeySecret string   `json:"accessKeySecret"` // AccessKeySecret 是服务商访问密钥秘密。
	Enabled         bool     `json:"enabled"`         // Enabled 表示签发任务是否可以使用该凭据。
}

// ListDNSCredentials 返回脱敏后的 DNS 凭据列表。
func (s *Services) ListDNSCredentials(ctx context.Context) ([]store.DNSCredential, error) {
	values, err := s.Store.ListDNSCredentials(ctx)
	if err != nil {
		return nil, s.internalError("查询 DNS 凭据失败", err)
	}
	for index := range values {
		if err := s.decryptDNS(&values[index]); err != nil {
			return nil, s.internalError("解密 DNS 凭据失败", err)
		}
		values[index].MaskedAccessKeyID = maskedDNSIdentifier(values[index].Provider, values[index].AccessKeyID)
		values[index].AccessKeyID, values[index].AccessKeySecret = "", ""
	}
	return values, nil
}

// CreateDNSCredential 校验并加密保存新的 DNS 凭据。
func (s *Services) CreateDNSCredential(ctx context.Context, userID int64, input DNSCredentialInput) (store.DNSCredential, error) {
	value, err := s.prepareDNSInput(input, nil)
	if err != nil {
		return store.DNSCredential{}, err
	}
	value, err = s.Store.CreateDNSCredential(ctx, value)
	if err != nil {
		return store.DNSCredential{}, s.internalError("创建 DNS 凭据失败", err)
	}
	s.RecordAudit(ctx, &userID, "CREATE", "DNS_CREDENTIAL", formatID(value.ID), "创建 DNS 凭据")
	value.MaskedAccessKeyID = maskedDNSIdentifier(value.Provider, input.AccessKeyID)
	value.AccessKeyID, value.AccessKeySecret = "", ""
	return value, nil
}

// UpdateDNSCredential 保留未重新输入的秘密并更新加密凭据。
func (s *Services) UpdateDNSCredential(ctx context.Context, userID, id int64, input DNSCredentialInput) (store.DNSCredential, error) {
	old, err := s.Store.FindCredential(ctx, id)
	if err != nil {
		return old, normalizeStoreError(err, "凭据不存在")
	}
	return s.SaveCredential(ctx, userID, id, CredentialInput{DNSCredentialInput: input, DNSEnabled: true, CloudEnabled: old.CloudEnabled})
}

// DeleteDNSCredential 仅删除未被订单或证书引用的 DNS 凭据。
func (s *Services) DeleteDNSCredential(ctx context.Context, userID, id int64) error {
	return s.DeleteCredential(ctx, userID, id)
}

// DNSCredentialTest 是逐根域名测试的汇总；部分失败作为业务结果返回，便于查看每一项。
type DNSCredentialTest struct {
	Success  bool                      `json:"success"`  // Success 仅在全部根域名通过时为 true。
	Message  string                    `json:"message"`  // Message 汇总通过与失败数量。
	TestedAt time.Time                 `json:"testedAt"` // TestedAt 是整次测试的开始时间。
	Results  []store.DNSZoneTestResult `json:"results"`  // Results 按根域名顺序保留所有结果。
}

// TestDNSCredential 逐个创建并清理临时 TXT 记录，测试结果与凭据配置快照绑定。
func (s *Services) TestDNSCredential(ctx context.Context, id int64) (DNSCredentialTest, error) {
	value, err := s.Store.FindCredential(ctx, id)
	if err != nil {
		return DNSCredentialTest{}, normalizeStoreError(err, "DNS 凭据不存在")
	}
	if !value.DNSEnabled {
		return DNSCredentialTest{}, BadRequest("凭据未启用 DNS 验证用途")
	}
	if err := s.decryptDNS(&value); err != nil {
		return DNSCredentialTest{}, s.internalError("解密 DNS 凭据失败", err)
	}
	if len(value.Zones) == 0 {
		return DNSCredentialTest{}, BadRequest("请先配置至少一个根域名")
	}
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// 按凭据服务商选择对应的根域名权限测试。
	result := testCredentialZones(testCtx, value.Zones, func(zoneCtx context.Context, zone string) error {
		if value.Provider == "CLOUDFLARE" {
			return dnsprovider.TestCloudflareCredential(zoneCtx, value.AccessKeySecret, zone)
		}
		return dnsprovider.TestAliyunCredential(zoneCtx, value.AccessKeyID, value.AccessKeySecret, zone)
	})
	if err := s.Store.RecordDNSZoneTests(ctx, value, result.Results); err != nil {
		if errors.Is(err, store.ErrSeriesChanged) {
			return result, Conflict("测试期间凭据已修改，请使用最新配置重新测试")
		}
		return result, s.internalError("保存 DNS 权限测试结果失败", err)
	}
	return result, nil
}

// testCredentialZones 持续测试剩余域名，隔离单项失败；仅返回固定安全文案，不透传服务商错误。
func testCredentialZones(ctx context.Context, zones []string, test func(context.Context, string) error) DNSCredentialTest {
	result := DNSCredentialTest{Success: len(zones) > 0, TestedAt: time.Now().UTC(), Results: []store.DNSZoneTestResult{}}
	passed := 0
	for _, zone := range zones {
		item := store.DNSZoneTestResult{Zone: zone, TestedAt: time.Now().UTC()}
		if ctx.Err() != nil {
			item.Message = "未完成测试：本次测试已取消或达到总时限，请重试"
		} else {
			zoneCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			err := test(zoneCtx, zone)
			cancel()
			item.Success = err == nil
			item.Message = "查询、写入和清理 TXT 记录权限正常"
			if err != nil {
				item.Message = "查询、TXT 写入或清理失败，请检查该根域名的服务商权限和网络"
			}
		}
		if item.Success {
			passed++
		} else {
			result.Success = false
		}
		result.Results = append(result.Results, item)
	}
	result.Message = fmt.Sprintf("已验证 %d 个根域名：%d 个通过，%d 个失败", len(zones), passed, len(zones)-passed)
	return result
}

// DecryptedDNSCredential 读取并解密一个已启用的 DNS 凭据供签发使用。
func (s *Services) DecryptedDNSCredential(ctx context.Context, id int64) (store.DNSCredential, error) {
	value, err := s.Store.FindDNSCredential(ctx, id)
	if err != nil {
		return store.DNSCredential{}, normalizeStoreError(err, "DNS 凭据不存在")
	}
	if !value.Enabled {
		return store.DNSCredential{}, BadRequest("所选 DNS 凭据已停用")
	}
	if err := s.decryptDNS(&value); err != nil {
		return store.DNSCredential{}, s.internalError("解密 DNS 凭据失败", err)
	}
	return value, nil
}

// prepareDNSInput 规范化凭据参数并生成绑定用途的加密信封。
func (s *Services) prepareDNSInput(input DNSCredentialInput, existing *store.DNSCredential) (store.DNSCredential, error) {
	return s.prepareProviderInput(input, existing, true)
}

// prepareProviderInput 统一校验和加密秘密，DNS 用途才要求根域名。
func (s *Services) prepareProviderInput(input DNSCredentialInput, existing *store.DNSCredential, dnsEnabled bool) (store.DNSCredential, error) {
	name := strings.TrimSpace(input.Name)
	zones := input.Zones
	if zones == nil {
		zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input.Zone)), ".")
		if existing != nil && len(existing.Zones) > 1 {
			if zone != "" && zone != existing.Zone {
				return store.DNSCredential{}, BadRequest("此凭据已配置多个根域名，请使用完整根域名列表修改")
			}
			zones = existing.Zones
		} else if zone != "" {
			zones = []string{zone}
		}
	}
	zones, err := dnsnames.NormalizeZones(zones)
	if err != nil {
		return store.DNSCredential{}, BadRequest(err.Error())
	}
	if name == "" {
		return store.DNSCredential{}, BadRequest("凭据名称不能为空")
	}
	if dnsEnabled && len(zones) == 0 {
		return store.DNSCredential{}, BadRequest("启用 DNS 验证用途时至少填写一个根域名")
	}
	zone := ""
	if len(zones) > 0 {
		zone = zones[0]
	}
	if input.Provider == "" {
		input.Provider = "ALIYUN"
	}
	if input.Provider != "ALIYUN" && input.Provider != "CLOUDFLARE" {
		return store.DNSCredential{}, BadRequest("仅支持阿里云 DNS 和 Cloudflare")
	}
	if existing != nil && existing.Provider != input.Provider {
		return store.DNSCredential{}, BadRequest("已有凭据不能变更服务商，请新增凭据后调整系列配置")
	}
	accessKeyID, secret := strings.TrimSpace(input.AccessKeyID), strings.TrimSpace(input.AccessKeySecret)
	if existing != nil {
		if accessKeyID == "" {
			accessKeyID = existing.AccessKeyID
		}
		if secret == "" {
			secret = existing.AccessKeySecret
		}
	}
	if input.Provider == "CLOUDFLARE" {
		accessKeyID = "api-token"
	}
	if accessKeyID == "" || secret == "" {
		return store.DNSCredential{}, BadRequest("阿里云需要 AccessKeyId 和 Secret，Cloudflare 需要 API Token")
	}
	idEnvelope, err := s.Vault.Encrypt("dns-access-key-id", []byte(accessKeyID))
	if err != nil {
		return store.DNSCredential{}, s.internalError("加密 DNS AccessKeyId 失败", err)
	}
	secretEnvelope, err := s.Vault.Encrypt("dns-access-key-secret", []byte(secret))
	if err != nil {
		return store.DNSCredential{}, s.internalError("加密 DNS AccessKeySecret 失败", err)
	}
	return store.DNSCredential{DNSEnabled: dnsEnabled, Name: name, Provider: input.Provider, Zone: zone, Zones: zones, AccessKeyID: idEnvelope, AccessKeySecret: secretEnvelope, Enabled: input.Enabled}, nil
}

// maskedDNSIdentifier 返回适合服务商类型的标识，不对 Token 或密文生成可见片段。
func maskedDNSIdentifier(provider, identifier string) string {
	if provider == "CLOUDFLARE" {
		return "API Token"
	}
	return store.MaskAccessKey(identifier)
}

// decryptDNS 就地解密仓库模型中的访问密钥字段。
func (s *Services) decryptDNS(value *store.DNSCredential) error {
	id, err := s.Vault.Decrypt("dns-access-key-id", value.AccessKeyID)
	if err != nil {
		return err
	}
	secret, err := s.Vault.Decrypt("dns-access-key-secret", value.AccessKeySecret)
	if err != nil {
		return err
	}
	value.AccessKeyID, value.AccessKeySecret = string(id), string(secret)
	return nil
}
