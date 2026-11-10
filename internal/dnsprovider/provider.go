// 本文件按显式凭据创建 DNS-01 适配器，避免服务商环境变量污染其他订单。
//
// @author Andersen
package dnsprovider

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-acme/lego/v5/challenge"
	"github.com/go-acme/lego/v5/providers/dns/alidns"
	"github.com/go-acme/lego/v5/providers/dns/cloudflare"
)

// NewChallengeProvider 根据持久化服务商类型创建隔离的 lego DNS Provider。
func NewChallengeProvider(provider, accessKeyID, secret string, propagationTimeout, pollingInterval time.Duration) (challenge.Provider, error) {
	switch provider {
	case "ALIYUN":
		configuration := alidns.NewDefaultConfig()
		configuration.APIKey, configuration.SecretKey = accessKeyID, secret
		configuration.PropagationTimeout, configuration.PollingInterval = propagationTimeout, pollingInterval
		return alidns.NewDNSProviderConfig(configuration)
	case "CLOUDFLARE":
		configuration := &cloudflare.Config{AuthToken: secret, ZoneToken: secret, BaseURL: cloudflareEndpoint, TTL: 120, PropagationTimeout: propagationTimeout, PollingInterval: pollingInterval, HTTPClient: cloudflareHTTPClient()}
		return cloudflare.NewDNSProviderConfig(configuration)
	default:
		return nil, fmt.Errorf("不支持的 DNS 服务商")
	}
}

// cloudflareHTTPClient 禁止带凭据请求重定向，并限制单次服务商访问时间。
func cloudflareHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second,
		// 令牌只允许发往固定 Cloudflare API，不跟随服务端重定向。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}
