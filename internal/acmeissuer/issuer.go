// Package acmeissuer 使用 lego 完成账户注册、DNS-01 验证和证书签发。
//
// @author Andersen
package acmeissuer

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/dnsprovider"
	"github.com/twodogleee/acme4go/internal/store"
	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/registration"
)

// account 实现 lego 所需的 ACME 用户账户接口。
type account struct {
	Email        string                // Email 是 CA 联系邮箱。
	Registration *acme.ExtendedAccount // Registration 是 ACME 服务端账户注册资源。
	PrivateKey   crypto.Signer         // PrivateKey 是账户请求签名私钥。
}

// GetEmail 返回 ACME 账户联系邮箱。
func (a *account) GetEmail() string { return a.Email }

// GetRegistration 返回 ACME 服务端注册资源。
func (a *account) GetRegistration() *acme.ExtendedAccount { return a.Registration }

// GetPrivateKey 返回 ACME 账户签名私钥。
func (a *account) GetPrivateKey() crypto.Signer { return a.PrivateKey }

// Issuer 持久化 ACME 账户并通过动态解密的服务商凭据完成签发。
type Issuer struct {
	store              *store.Store      // store 持久化 ACME 账户与签发结果。
	vault              *cryptoutil.Vault // vault 加解密账户私钥。
	propagationTimeout time.Duration     // propagationTimeout 是 DNS 传播等待上限。
	pollingInterval    time.Duration     // pollingInterval 是 DNS 验证轮询间隔。
}

// New 创建使用指定仓库、保险库和 DNS 等待参数的签发器。
func New(database *store.Store, vault *cryptoutil.Vault, propagationTimeout, pollingInterval time.Duration) *Issuer {
	return &Issuer{store: database, vault: vault, propagationTimeout: propagationTimeout, pollingInterval: pollingInterval}
}

// Issue 完成一次证书签发；stage 回调用于持久化可恢复的业务阶段。
func (i *Issuer) Issue(ctx context.Context, order store.Order, credential store.DNSCredential, stage func(string)) (deploy.Bundle, error) {
	stage("PREPARING_ACCOUNT")
	user, accountRecord, err := i.loadOrCreateAccount(ctx, order.Email, order.CADirectory)
	if err != nil {
		return deploy.Bundle{}, err
	}
	config := lego.NewConfig(user)
	config.CADirURL = order.CADirectory
	client, err := lego.NewClient(config)
	if err != nil {
		return deploy.Bundle{}, fmt.Errorf("创建 ACME 客户端: %w", err)
	}
	provider, err := dnsprovider.NewChallengeProvider(credential.Provider, credential.AccessKeyID, credential.AccessKeySecret, i.propagationTimeout, i.pollingInterval)
	if err != nil {
		return deploy.Bundle{}, fmt.Errorf("创建 DNS Provider: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return deploy.Bundle{}, fmt.Errorf("设置 DNS-01 Provider: %w", err)
	}
	if user.Registration == nil {
		registrationResource, err := client.Registration.Register(ctx, registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return deploy.Bundle{}, fmt.Errorf("注册 ACME 账户: %w", err)
		}
		user.Registration = registrationResource
		registrationJSON, err := json.Marshal(registrationResource)
		if err != nil {
			return deploy.Bundle{}, err
		}
		registrationText := string(registrationJSON)
		accountRecord.RegistrationJSON = &registrationText
		if err := i.store.PutACMEAccount(ctx, accountRecord); err != nil {
			return deploy.Bundle{}, err
		}
	}

	stage("CREATING_ORDER")
	stage("AUTHORIZING_DNS")
	resource, err := client.Certificate.Obtain(ctx, certificate.ObtainRequest{
		Domains: order.Domains,
		KeyType: certcrypto.EC256,
		Bundle:  true,
	})
	if err != nil {
		return deploy.Bundle{}, fmt.Errorf("ACME 证书签发失败: %w", err)
	}
	stage("ISSUING")
	block, _ := pem.Decode(resource.Certificate)
	if block == nil || block.Type != "CERTIFICATE" {
		return deploy.Bundle{}, errors.New("CA 返回的叶证书格式无效")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return deploy.Bundle{}, fmt.Errorf("解析叶证书: %w", err)
	}
	fullChain := append([]byte(nil), resource.Certificate...)
	if len(resource.IssuerCertificate) > 0 && !strings.Contains(string(fullChain), string(resource.IssuerCertificate)) {
		fullChain = append(fullChain, resource.IssuerCertificate...)
	}
	fingerprintBytes := sha256.Sum256(leaf.Raw)
	commonName := leaf.Subject.CommonName
	if commonName == "" {
		commonName = order.Domains[0]
	}
	return deploy.Bundle{
		Certificate:  resource.Certificate,
		FullChain:    fullChain,
		PrivateKey:   resource.PrivateKey,
		Fingerprint:  strings.ToUpper(hex.EncodeToString(fingerprintBytes[:])),
		CommonName:   commonName,
		SANs:         append([]string(nil), leaf.DNSNames...),
		Issuer:       leaf.Issuer.String(),
		SerialNumber: leaf.SerialNumber.Text(16),
		NotBefore:    leaf.NotBefore.UTC(),
		NotAfter:     leaf.NotAfter.UTC(),
	}, nil
}

// loadOrCreateAccount 按邮箱和 CA 目录复用或创建加密持久化的 ACME 账户。
func (i *Issuer) loadOrCreateAccount(ctx context.Context, email, directory string) (*account, store.ACMEAccount, error) {
	keyBytes := sha256.Sum256([]byte(strings.ToLower(email) + "\x00" + directory))
	accountKey := hex.EncodeToString(keyBytes[:])
	record, err := i.store.GetACMEAccount(ctx, accountKey)
	if err == nil {
		privatePEM, err := i.vault.Decrypt("acme-account:"+accountKey, record.PrivateKey)
		if err != nil {
			return nil, store.ACMEAccount{}, err
		}
		privateKey, err := parsePrivateKey(privatePEM)
		if err != nil {
			return nil, store.ACMEAccount{}, err
		}
		user := &account{Email: email, PrivateKey: privateKey}
		if record.RegistrationJSON != nil {
			var registrationResource acme.ExtendedAccount
			if err := json.Unmarshal([]byte(*record.RegistrationJSON), &registrationResource); err != nil {
				return nil, store.ACMEAccount{}, err
			}
			user.Registration = &registrationResource
		}
		return user, record, nil
	}
	if err != store.ErrNotFound {
		return nil, store.ACMEAccount{}, err
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, store.ACMEAccount{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, store.ACMEAccount{}, err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	encrypted, err := i.vault.Encrypt("acme-account:"+accountKey, privatePEM)
	if err != nil {
		return nil, store.ACMEAccount{}, err
	}
	record = store.ACMEAccount{Key: accountKey, Email: email, CADirectory: directory, PrivateKey: encrypted}
	if err := i.store.PutACMEAccount(ctx, record); err != nil {
		return nil, store.ACMEAccount{}, err
	}
	return &account{Email: email, PrivateKey: privateKey}, record, nil
}

// parsePrivateKey 将 PKCS#8 PEM 解析为 ACME 可用的签名器。
func parsePrivateKey(content []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("ACME 账户私钥 PEM 无效")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("ACME 账户私钥不支持签名")
	}
	return signer, nil
}
