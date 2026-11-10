// 本文件以模拟云端验证上传幂等、敏感材料边界及 OSS/CDN 更新语义。
//
// @author Andersen
package aliyun

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/cloud"
)

// rpcFunc 将测试回调适配为云 API 边界。
type rpcFunc func(string, map[string]string) (map[string]any, error)

// Call 将请求交给模拟云端。
func (f rpcFunc) Call(_ context.Context, _ cloud.Credentials, _, _, action string, values map[string]string) (map[string]any, error) {
	return f(action, values)
}

// roundTripFunc 在内存中检查官方 OSS SDK 生成的请求。
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 模拟 HTTP 传输而不访问真实云账号。
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestUploadRecoversRemoteSuccess 验证远程已有相同指纹时不会再次上传。
func TestUploadRecoversRemoteSuccess(t *testing.T) {
	calls := 0
	fingerprint := strings.Repeat("ab", 32)
	// 列表返回先前成功上传的记录，模拟本地落库前进程中断。
	c := Catalog{RPC: rpcFunc(func(action string, params map[string]string) (map[string]any, error) {
		calls++
		switch action {
		case "ListUserCertificateOrder":
			if params["OrderType"] != "CERT" {
				t.Fatal("必须查询证书列表")
			}
			return map[string]any{"TotalCount": 1, "CertificateOrderList": []any{map[string]any{"CertificateId": "123", "Sha2": strings.ToUpper(fingerprint), "Name": "prior"}}}, nil
		case "GetUserCertificateDetail":
			if params["CertId"] != "123" || params["CertFilter"] != "true" {
				t.Fatal("必须按证书 ID 查询并过滤私钥")
			}
			return map[string]any{"Id": "123", "CertSha2": fingerprint}, nil
		default:
			t.Fatalf("恢复已有证书不应调用 %s", action)
			return nil, nil
		}
	})}
	cert, err := c.Upload(context.Background(), cloud.Credentials{}, cloud.Material{Name: "new", Fingerprint: fingerprint, Key: []byte("private")})
	if err != nil || cert.ID != "123" || calls != 2 {
		t.Fatalf("幂等恢复失败 %+v %v %d", cert, err, calls)
	}
}

// TestCatalogUploadAndFilteredGet 验证私钥仅发送到上传接口且已有证书查询过滤敏感内容。
func TestCatalogUploadAndFilteredGet(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	// 上传返回新 ID，详情只返回证书元数据。
	c := Catalog{RPC: rpcFunc(func(action string, params map[string]string) (map[string]any, error) {
		switch action {
		case "ListUserCertificateOrder":
			return map[string]any{"TotalCount": 0, "CertificateOrderList": []any{}}, nil
		case "UploadUserCertificate":
			if params["Key"] != "private" || params["Cert"] != "chain" || params["ClientToken"] != fingerprint {
				t.Fatal("上传材料或幂等标识不匹配")
			}
			return map[string]any{"CertId": "123"}, nil
		case "GetUserCertificateDetail":
			if params["CertFilter"] != "true" {
				t.Fatal("查询必须过滤私钥")
			}
			return map[string]any{"Id": "123", "Sha2": strings.ToUpper(fingerprint), "Sans": "*.example.com,example.com", "EndDate": "2099-01-01"}, nil
		default:
			t.Fatalf("意外 API %s", action)
			return nil, nil
		}
	})}
	cert, err := c.Upload(context.Background(), cloud.Credentials{}, cloud.Material{Name: "cert", Fingerprint: fingerprint, Chain: []byte("chain"), Key: []byte("private")})
	if err != nil {
		t.Fatal(err)
	}
	cert, err = c.Get(context.Background(), cloud.Credentials{}, cert.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Covers("cdn.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := cert.Covers("a.b.example.com"); err == nil {
		t.Fatal("不能跨层匹配通配符")
	}
}

// TestCDNChecksExactBinding 验证仅接受新证书已启用且成功生效的状态。
func TestCDNChecksExactBinding(t *testing.T) {
	status, id := "checking", "123"
	// 云端先返回检测中，再返回成功，最后模拟被外部替换。
	d := CDN{RPC: rpcFunc(func(action string, params map[string]string) (map[string]any, error) {
		if action == "SetCdnDomainSSLCertificate" {
			if params["CertType"] != "cas" || params["CertId"] != "123" || params["SSLProtocol"] != "on" || params["SSLPri"] != "" {
				t.Fatal("部署必须引用云证书")
			}
			return map[string]any{}, nil
		}
		return map[string]any{"CertInfos": map[string]any{"CertInfo": []any{map[string]any{"DomainName": "cdn.example.com", "CertId": id, "Status": status, "ServerCertificateStatus": "on"}}}}, nil
	})}
	raw := json.RawMessage(`{"domain":"cdn.example.com"}`)
	cert := cloud.Certificate{ID: "123"}
	if err := d.Deploy(context.Background(), cloud.Credentials{}, raw, cert); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.Verify(context.Background(), cloud.Credentials{}, raw, cert); ok {
		t.Fatal("检测中不能视为成功")
	}
	status = "success"
	if ok, _ := d.Verify(context.Background(), cloud.Credentials{}, raw, cert); !ok {
		t.Fatal("成功状态应通过")
	}
	id = "999"
	if ok, _ := d.Verify(context.Background(), cloud.Credentials{}, raw, cert); ok {
		t.Fatal("旧证书不能视为成功")
	}
}

// TestOSSUsesPreviousIDAndNeverUploadsPrivateKey 验证官方 SDK 使用 V4 签名和乐观并发保护。
func TestOSSUsesPreviousIDAndNeverUploadsPrivateKey(t *testing.T) {
	puts := 0
	boundID := "111-cn-hangzhou"
	// 内存传输模拟 OSS 域名查询和证书替换。
	d := OSS{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "my-bucket.oss-cn-hangzhou.aliyuncs.com" || r.URL.Scheme != "https" {
			t.Fatalf("错误目标 %s", r.URL)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "OSS4-HMAC-SHA256") {
			t.Fatal("必须使用官方 V4 签名")
		}
		body := ""
		if r.Method == "POST" {
			puts++
			payload, _ := io.ReadAll(r.Body)
			text := string(payload)
			if !strings.Contains(text, "<PreviousCertId>111-cn-hangzhou</PreviousCertId>") || !strings.Contains(text, "<Force>false</Force>") || !strings.Contains(text, "<CertId>222-cn-hangzhou</CertId>") || strings.Contains(text, "<PrivateKey>") {
				t.Fatalf("错误证书配置 %s", text)
			}
			boundID = "222-cn-hangzhou"
		} else {
			body = `<ListCnameResult><Cname><Domain>files.example.com</Domain><Status>Enabled</Status><Certificate><CertId>` + boundID + `</CertId><Status>Enabled</Status></Certificate></Cname></ListCnameResult>`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	raw := json.RawMessage(`{"domain":"files.example.com","bucket":"my-bucket","region":"cn-hangzhou"}`)
	credential := cloud.Credentials{AccessKeyID: "test-key", AccessKeySecret: "test-secret"}
	cert := cloud.Certificate{ID: "222"}
	if err := d.Deploy(context.Background(), credential, raw, cert); err != nil {
		t.Fatal(err)
	}
	if err := d.Deploy(context.Background(), credential, raw, cert); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Verify(context.Background(), credential, raw, cert); err != nil || !ok || puts != 1 {
		t.Fatalf("幂等部署验证失败 %v %v puts=%d", ok, err, puts)
	}
}

// TestProductValidationRejectsArbitraryEndpoints 验证用户输入不能改变签名请求主机或携带未知参数。
func TestProductValidationRejectsArbitraryEndpoints(t *testing.T) {
	for _, raw := range []string{`{"domain":"https://evil.test"}`, `{"domain":"*.example.com"}`, `{"domain":"cdn.example.com","endpoint":"https://evil.test"}`} {
		if _, _, err := (&CDN{}).Validate(json.RawMessage(raw)); err == nil {
			t.Fatalf("应拒绝 %s", raw)
		}
	}
	if _, _, err := (&OSS{}).Validate(json.RawMessage(`{"domain":"files.example.com","bucket":"my-bucket","region":"cn-hangzhou.evil.test/"}`)); err == nil {
		t.Fatal("应拒绝任意主机")
	}
}
