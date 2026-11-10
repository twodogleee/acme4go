// Package cloud 定义云证书仓库和云产品部署的独立契约，不依赖数据库或应用服务。
//
// @author Andersen
package cloud

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"time"
)

// Credentials 仅在调用期间持有解密的云账号凭证。
type Credentials struct {
	AccessKeyID     string // AccessKeyID 是访问密钥标识。
	AccessKeySecret string // AccessKeySecret 是访问密钥秘密。
}

// Certificate 是可供部署选择的云端证书元数据，不含私钥。
type Certificate struct {
	ID          string   `json:"id"`          // ID 是云端证书标识，使用字符串避免精度损失。
	Name        string   `json:"name"`        // Name 是云端证书名称。
	Domains     []string `json:"domains"`     // Domains 是证书覆盖的域名。
	Fingerprint string   `json:"fingerprint"` // Fingerprint 是 SHA-256 指纹。
	NotAfter    string   `json:"notAfter"`    // NotAfter 是云端返回的到期日期。
}

// CertificatePage 保留云端分页语义。
type CertificatePage struct {
	Items []Certificate `json:"items"` // Items 是当前页有效证书。
	Total int           `json:"total"` // Total 是匹配证书数。
}

// Material 是上传所需的内存材料，不允许持久化到任务或日志。
type Material struct {
	Name        string // Name 是可重试的确定性名称。
	Chain       []byte // Chain 是叶证书及中间证书链。
	Key         []byte // Key 是匹配私钥。
	Fingerprint string // Fingerprint 用于查重和请求幂等。
}

// Catalog 将云证书的上传与查询从产品部署中分离。
type Catalog interface {
	// List 分页查询云端已有的有效证书。
	List(context.Context, Credentials, int, string) (CertificatePage, error)
	// Upload 幂等上传证书并返回云端标识。
	Upload(context.Context, Credentials, Material) (Certificate, error)
	// Get 验证已有云证书，返回不含秘密的元数据。
	Get(context.Context, Credentials, string) (Certificate, error)
}

// Field 描述产品特有的目标配置，供界面按注册表渲染。
type Field struct {
	Key         string `json:"key"`         // Key 是配置字段名。
	Label       string `json:"label"`       // Label 是表单名称。
	Placeholder string `json:"placeholder"` // Placeholder 是输入示例。
}

// Product 描述可扩展云产品及其输入契约。
type Product struct {
	ID     string  `json:"id"`     // ID 是稳定适配器标识。
	Name   string  `json:"name"`   // Name 是产品名称。
	Fields []Field `json:"fields"` // Fields 是产品所需配置字段。
}

// Deployer 仅更新已存在的云产品资源，不上传证书或创建业务资源。
type Deployer interface {
	// Product 返回适配器的配置描述。
	Product() Product
	// Validate 校验并规范化配置，返回目标域名及规范配置。
	Validate(json.RawMessage) (string, json.RawMessage, error)
	// Deploy 更新云产品证书，必须允许相同证书重复执行。
	Deploy(context.Context, Credentials, json.RawMessage, Certificate) error
	// Verify 查询云产品配置是否已经切换至指定证书。
	Verify(context.Context, Credentials, json.RawMessage, Certificate) (bool, error)
}

// Verification 保留云产品核验结论和可安全展示的诊断，不包含证书材料或凭据。
type Verification struct {
	Verified bool   // Verified 表示证书身份和产品启用状态均已确认。
	Message  string // Message 是适配器生成的安全说明，禁止直接使用原始 API 响应。
}

// DetailedVerifier 为需要解释等待原因的产品提供可选能力，兼容已有部署适配器。
type DetailedVerifier interface {
	// VerifyDetailed 查询当前绑定并解释未通过或已确认的条件。
	VerifyDetailed(context.Context, Credentials, json.RawMessage, Certificate) (Verification, error)
}

// VerifyDeployment 优先获取详细核验结论，旧适配器继续使用原有校验规则。
func VerifyDeployment(ctx context.Context, adapter Deployer, credential Credentials, config json.RawMessage, cert Certificate) (Verification, error) {
	if detailed, ok := adapter.(DetailedVerifier); ok {
		return detailed.VerifyDetailed(ctx, credential, config, cert)
	}
	verified, err := adapter.Verify(ctx, credential, config, cert)
	message := "尚未确认目标证书已启用"
	if verified {
		message = "目标证书已启用"
	}
	return Verification{Verified: verified, Message: message}, err
}

// Registry 以产品标识注册部署适配器，应用层无需按产品分支。
type Registry struct {
	Catalog   Catalog             // Catalog 是当前云证书仓库。
	Deployers map[string]Deployer // Deployers 是产品标识到适配器的映射。
}

// Covers 校验目标域名和到期时间，防止部署不匹配或已过期证书。
func (c Certificate) Covers(domain string) error {
	cert := &x509.Certificate{DNSNames: c.Domains}
	if err := cert.VerifyHostname(domain); err != nil {
		return errors.New("证书未覆盖目标域名")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if expiry, err := time.Parse(layout, c.NotAfter); err == nil {
			if !expiry.After(time.Now()) {
				return errors.New("云证书已过期")
			}
			return nil
		}
	}
	return errors.New("无法确认云证书有效期")
}
