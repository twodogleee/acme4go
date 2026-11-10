// 本文件分别实现 CDN 与 OSS 证书部署，产品特有参数和验证规则封装在适配器内。
//
// @author Andersen
package aliyun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

// targetConfig 保存两个适配器当前使用的资源定位字段。
type targetConfig struct {
	Domain string `json:"domain"`           // Domain 是实际终止 TLS 的精确域名。
	Region string `json:"region,omitempty"` // Region 是 OSS Bucket 地域。
	Bucket string `json:"bucket,omitempty"` // Bucket 是已有普通 Bucket 名称。
}

// dnsPattern 排除 URL、端口和通配符；IP 另行通过 net.ParseIP 拒绝。
var dnsPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// bucketPattern 限制 Bucket 名称，防止影响 SDK 请求主机。
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// regionPattern 限制地域标识，禁止用户输入任意 Endpoint。
var regionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// parseConfig 对产品配置执行严格解码。
func parseConfig(raw json.RawMessage, isOSS bool) (targetConfig, json.RawMessage, error) {
	var cfg targetConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, nil, errors.New("云产品配置格式无效")
	}
	cfg.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cfg.Domain), "."))
	cfg.Region, cfg.Bucket = strings.TrimSpace(cfg.Region), strings.TrimSpace(cfg.Bucket)
	if len(cfg.Domain) > 253 || !dnsPattern.MatchString(cfg.Domain) || net.ParseIP(cfg.Domain) != nil {
		return cfg, nil, errors.New("请输入不含协议、端口或通配符的完整域名")
	}
	if isOSS {
		if !bucketPattern.MatchString(cfg.Bucket) || !regionPattern.MatchString(cfg.Region) {
			return cfg, nil, errors.New("OSS Bucket 或地域格式无效")
		}
	} else if cfg.Region != "" || cfg.Bucket != "" {
		return cfg, nil, errors.New("CDN 目标只需要加速域名")
	}
	encoded, err := json.Marshal(cfg)
	return cfg, encoded, err
}

// CDN 更新已接入阿里云 CDN 的域名证书。
type CDN struct {
	RPC RPC // RPC 执行 CDN OpenAPI 调用。
}

// Product 描述 CDN 域名定位字段。
func (*CDN) Product() cloud.Product {
	return cloud.Product{ID: "CDN", Name: "阿里云 CDN", Fields: []cloud.Field{{Key: "domain", Label: "加速域名", Placeholder: "cdn.example.com"}}}
}

// Validate 规范化 CDN 目标域名。
func (*CDN) Validate(raw json.RawMessage) (string, json.RawMessage, error) {
	cfg, data, err := parseConfig(raw, false)
	return cfg.Domain, data, err
}

// Deploy 通过 CAS ID 更新 CDN 证书，不传递私钥或修改其他加速配置。
func (d *CDN) Deploy(ctx context.Context, credential cloud.Credentials, raw json.RawMessage, cert cloud.Certificate) error {
	cfg, _, err := parseConfig(raw, false)
	if err != nil {
		return err
	}
	_, err = d.RPC.Call(ctx, credential, "cdn.aliyuncs.com", "2018-05-10", "SetCdnDomainSSLCertificate", map[string]string{"DomainName": cfg.Domain, "CertType": "cas", "CertId": cert.ID, "CertName": cert.Name, "CertRegion": "cn-hangzhou", "SSLProtocol": "on"})
	return err
}

// Verify 兼容部署契约，具体证书身份及启用状态检查由详细核验负责。
func (d *CDN) Verify(ctx context.Context, credential cloud.Credentials, raw json.RawMessage, cert cloud.Certificate) (bool, error) {
	result, err := d.VerifyDetailed(ctx, credential, raw, cert)
	return result.Verified, err
}

// OSS 更新已有普通 Bucket 自定义域名证书，不创建 Bucket 或域名绑定。
type OSS struct {
	HTTPClient *http.Client // HTTPClient 可注入测试传输；生产使用限时 HTTPS 客户端。
}

// Product 描述 OSS 的 Bucket、地域和自定义域名。
func (*OSS) Product() cloud.Product {
	return cloud.Product{ID: "OSS", Name: "阿里云 OSS", Fields: []cloud.Field{{Key: "domain", Label: "自定义域名", Placeholder: "files.example.com"}, {Key: "bucket", Label: "Bucket 名称", Placeholder: "my-bucket"}, {Key: "region", Label: "Bucket 地域", Placeholder: "cn-hangzhou"}}}
}

// Validate 验证资源定位配置。
func (*OSS) Validate(raw json.RawMessage) (string, json.RawMessage, error) {
	cfg, data, err := parseConfig(raw, true)
	return cfg.Domain, data, err
}

// client 创建启用 V4 签名、固定官方地址和超时的 OSS SDK 客户端。
func (d *OSS) client(cfg targetConfig, credential cloud.Credentials) *oss.Client {
	httpClient := d.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 25 * time.Second,
			// 禁止证书配置请求重定向到其他地址。
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return oss.NewClient(oss.LoadDefaultConfig().WithRegion(cfg.Region).WithEndpoint("https://oss-" + cfg.Region + ".aliyuncs.com").WithCredentialsProvider(credentials.NewStaticCredentialsProvider(credential.AccessKeyID, credential.AccessKeySecret)).WithHttpClient(httpClient))
}

// binding 读取目标域名当前证书，拒绝缺失、禁用和泛域名绑定。
func (d *OSS) binding(ctx context.Context, client *oss.Client, cfg targetConfig) (*oss.CnameCertificate, error) {
	result, err := client.ListCname(ctx, &oss.ListCnameRequest{Bucket: oss.Ptr(cfg.Bucket)})
	if err != nil {
		return nil, errors.New("OSS 域名查询失败，请检查地域、Bucket 和 oss:ListCname 权限")
	}
	for _, item := range result.Cnames {
		if oss.ToString(item.Domain) == cfg.Domain && oss.ToString(item.Status) == "Enabled" && (item.IsWildCard == nil || !*item.IsWildCard) {
			return item.Certificate, nil
		}
	}
	return nil, errors.New("OSS 中不存在已启用的精确域名绑定，请先在 OSS 配置自定义域名")
}

// Deploy 优先使用 PreviousCertId 防止覆盖并发变更；首次绑定证书才使用 Force。
func (d *OSS) Deploy(ctx context.Context, credential cloud.Credentials, raw json.RawMessage, cert cloud.Certificate) error {
	cfg, _, err := parseConfig(raw, true)
	if err != nil {
		return err
	}
	client := d.client(cfg, credential)
	previous, err := d.binding(ctx, client, cfg)
	if err != nil {
		return err
	}
	identifier := cert.ID + "-cn-hangzhou"
	if previous != nil && oss.ToString(previous.CertId) == identifier && oss.ToString(previous.Status) == "Enabled" {
		return nil
	}
	configuration := &oss.CertificateConfiguration{CertId: oss.Ptr(identifier)}
	if previous != nil && oss.ToString(previous.CertId) != "" {
		configuration.PreviousCertId = previous.CertId
		configuration.Force = oss.Ptr(false)
	} else {
		configuration.Force = oss.Ptr(true)
	}
	_, err = client.PutCname(ctx, &oss.PutCnameRequest{Bucket: oss.Ptr(cfg.Bucket), BucketCnameConfiguration: &oss.BucketCnameConfiguration{Cname: &oss.Cname{Domain: oss.Ptr(cfg.Domain), IsWildCard: oss.Ptr(false), CertificateConfiguration: configuration}}})
	if err != nil {
		return errors.New("OSS 证书更新失败，请检查证书权限或是否存在并发修改")
	}
	return nil
}

// Verify 查询 OSS 配置确认指定云证书已启用。
func (d *OSS) Verify(ctx context.Context, credential cloud.Credentials, raw json.RawMessage, cert cloud.Certificate) (bool, error) {
	cfg, _, err := parseConfig(raw, true)
	if err != nil {
		return false, err
	}
	current, err := d.binding(ctx, d.client(cfg, credential), cfg)
	if err != nil {
		return false, err
	}
	return current != nil && oss.ToString(current.CertId) == cert.ID+"-cn-hangzhou" && oss.ToString(current.Status) == "Enabled", nil
}
