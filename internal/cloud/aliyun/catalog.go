// Package aliyun 使用阿里云官方 SDK 实现证书中心与产品适配器。
// 当前证书中心固定为中国站 cn-hangzhou；OSS 资源地域由目标配置决定。
//
// @author Andersen
package aliyun

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cloud"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
)

// RPC 隔离 SDK 调用，便于适配器使用模拟云端验证行为。
type RPC interface {
	// Call 执行一个 RPC 操作并仅返回响应业务体。
	Call(context.Context, cloud.Credentials, string, string, string, map[string]string) (map[string]any, error)
}

// SDKRPC 使用官方通用 SDK 处理签名和 POST 请求。
type SDKRPC struct{}

// contextClient 为不直接接受 context 的 SDK 注入请求取消与禁止重定向边界。
type contextClient struct {
	ctx context.Context // ctx 是调用方生命周期。
}

// Call 使用 SDK 提供的传输配置发送带上下文的 HTTPS 请求。
func (c contextClient) Call(request *http.Request, transport *http.Transport) (*http.Response, error) {
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second,
		// 云端重定向不能将凭证或证书材料发送至其他地址。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client.Do(request.WithContext(c.ctx))
}

// Call 将所有参数放入表单正文；SDK 原始错误可能含响应或凭证，不能直接透出。
func (SDKRPC) Call(ctx context.Context, credential cloud.Credentials, endpoint, version, action string, values map[string]string) (map[string]any, error) {
	client, err := openapi.NewClient(&openapi.Config{AccessKeyId: dara.String(credential.AccessKeyID), AccessKeySecret: dara.String(credential.AccessKeySecret), Endpoint: dara.String(endpoint), RegionId: dara.String("cn-hangzhou"), HttpClient: contextClient{ctx: ctx}})
	if err != nil {
		return nil, errors.New("阿里云 SDK 初始化失败")
	}
	body := make(map[string]any, len(values))
	for key, value := range values {
		body[key] = value
	}
	response, err := client.CallApi(&openapi.Params{Action: dara.String(action), Version: dara.String(version), Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String("POST"), AuthType: dara.String("AK"), BodyType: dara.String("json"), ReqBodyType: dara.String("formData"), Style: dara.String("RPC")}, &openapi.OpenApiRequest{Body: body}, &dara.RuntimeOptions{Autoretry: dara.Bool(false)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("阿里云 %s 调用失败，请检查账号权限、资源状态和网络", action)
	}
	result, ok := response["body"].(map[string]any)
	if !ok {
		return nil, errors.New("阿里云返回了无效响应")
	}
	return result, nil
}

// Catalog 管理中国站证书中心中的外部上传证书。
type Catalog struct {
	RPC RPC // RPC 是可替换的云 API 调用边界。
}

// NewRegistry 注册证书仓库和当前支持的两个产品。
func NewRegistry() *cloud.Registry {
	rpc := SDKRPC{}
	return &cloud.Registry{Catalog: &Catalog{RPC: rpc}, Deployers: map[string]cloud.Deployer{"CDN": &CDN{RPC: rpc}, "OSS": &OSS{}}}
}

// text 将 SDK JSON 中的标识转换为文本。
func text(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	default:
		return ""
	}
}

// certificate 从云端响应中提取允许返回给界面的元数据。
func certificate(item map[string]any) cloud.Certificate {
	id := text(item["CertificateId"])
	if id == "" {
		id = text(item["Id"])
	}
	domains := strings.FieldsFunc(text(item["Sans"]),
		// 兼容逗号和空白分隔的 SAN 列表。
		func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	if len(domains) == 0 {
		common := text(item["Common"])
		if common == "" {
			common = text(item["CommonName"])
		}
		if common != "" {
			domains = []string{common}
		}
	}
	// 详情优先使用 CertSha2；旧响应和列表仍使用 Sha2，不能混入 SHA-1 的 Fingerprint。
	fingerprint := text(item["CertSha2"])
	if strings.TrimSpace(fingerprint) == "" {
		fingerprint = text(item["Sha2"])
	}
	return cloud.Certificate{ID: id, Name: text(item["Name"]), Domains: domains, Fingerprint: normalizeFingerprint(fingerprint), NotAfter: text(item["EndDate"])}
}

// normalizeFingerprint 将常见展示格式转换为完整 SHA-256 十六进制指纹；缺失或无效时返回空串。
func normalizeFingerprint(value string) string {
	value = strings.NewReplacer(":", "", " ", "", "\t", "", "\r", "", "\n", "").Replace(value)
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return strings.ToUpper(value)
}

// List 只查询有效证书，并保留云端分页；上传和已有签发证书都可选择。
func (c *Catalog) List(ctx context.Context, credential cloud.Credentials, page int, keyword string) (cloud.CertificatePage, error) {
	body, err := c.RPC.Call(ctx, credential, "cas.aliyuncs.com", "2020-04-07", "ListUserCertificateOrder", map[string]string{"OrderType": "CERT", "CurrentPage": strconv.Itoa(page), "ShowSize": "50", "Keyword": keyword})
	result := cloud.CertificatePage{Items: []cloud.Certificate{}}
	if err != nil {
		return result, err
	}
	result.Total, _ = strconv.Atoi(text(body["TotalCount"]))
	items, _ := body["CertificateOrderList"].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			result.Items = append(result.Items, certificate(item))
		}
	}
	return result, nil
}

// Get 使用 CertFilter 阻止云端返回私钥，校验只依赖元数据。
func (c *Catalog) Get(ctx context.Context, credential cloud.Credentials, id string) (cloud.Certificate, error) {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil || id == "0" {
		return cloud.Certificate{}, errors.New("云证书 ID 必须是正整数")
	}
	body, err := c.RPC.Call(ctx, credential, "cas.aliyuncs.com", "2020-04-07", "GetUserCertificateDetail", map[string]string{"CertId": id, "CertFilter": "true"})
	if err != nil {
		return cloud.Certificate{}, err
	}
	result := certificate(body)
	if result.ID != id {
		return result, errors.New("阿里云证书查询结果与请求不一致")
	}
	if expired, _ := body["Expired"].(bool); expired {
		return result, errors.New("所选阿里云证书已过期")
	}
	if result.Fingerprint == "" {
		return result, errors.New("阿里云证书已存在，但未返回有效的 SHA-256 指纹，无法完成证书校验")
	}
	return result, nil
}

// Upload 以确定名称和指纹查重，覆盖上传成功但本地登记前进程退出的恢复窗口。
func (c *Catalog) Upload(ctx context.Context, credential cloud.Credentials, material cloud.Material) (cloud.Certificate, error) {
	fingerprint := normalizeFingerprint(material.Fingerprint)
	if fingerprint == "" {
		return cloud.Certificate{}, errors.New("本地证书 SHA-256 指纹无效")
	}
	for page := 1; ; page++ {
		result, err := c.List(ctx, credential, page, "")
		if err != nil {
			return cloud.Certificate{}, err
		}
		for _, item := range result.Items {
			if item.Fingerprint == fingerprint || item.Name == material.Name {
				// 列表与详情可能使用不同的 SHA2 字段，恢复上传必须以详情再次确认。
				detail, err := c.Get(ctx, credential, item.ID)
				if err != nil {
					return cloud.Certificate{}, err
				}
				if detail.Fingerprint == fingerprint {
					return detail, nil
				}
				if item.Name == material.Name {
					return cloud.Certificate{}, errors.New("云端已有同名但 SHA-256 指纹不同的证书，已停止上传")
				}
			}
		}
		if page*50 >= result.Total {
			break
		}
		if page >= 200 {
			return cloud.Certificate{}, errors.New("云证书数量超过自动查重上限，请缩小账号管理范围")
		}
	}
	body, err := c.RPC.Call(ctx, credential, "cas.aliyuncs.com", "2020-04-07", "UploadUserCertificate", map[string]string{"Name": material.Name, "Cert": string(material.Chain), "Key": string(material.Key), "ClientToken": material.Fingerprint})
	if err != nil {
		return cloud.Certificate{}, err
	}
	id := text(body["CertId"])
	if id == "" {
		return cloud.Certificate{}, errors.New("阿里云未返回上传证书 ID")
	}
	return cloud.Certificate{ID: id, Name: material.Name, Fingerprint: fingerprint}, nil
}
