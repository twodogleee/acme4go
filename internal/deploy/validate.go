// 本文件统一校验证书部署前的完整性，供 Server 本机执行器和远程 Agent 复用。
//
// @author Andersen
package deploy

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"sort"
	"strings"
	"time"
)

// ValidateBundle 校验证书指纹、域名、有效期以及证书与私钥的匹配关系。
func ValidateBundle(bundle Bundle, commonName, fingerprint string) error {
	return ValidateBundleDomains(bundle, []string{commonName}, fingerprint, false)
}

// ValidateBundleDomains 校验证书材料，并可要求 SAN 与系列域名集合完全一致。
func ValidateBundleDomains(bundle Bundle, domains []string, fingerprint string, exact bool) error {
	block, _ := pem.Decode(bundle.Certificate)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("叶证书 PEM 无效")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(certificate.Raw)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), fingerprint) {
		return errors.New("证书指纹不匹配")
	}
	if exact {
		if !equalDomainSets(certificate.DNSNames, domains) {
			return errors.New("证书 SAN 与部署系列域名集合不一致")
		}
	} else {
		foundName := false
		for _, name := range certificate.DNSNames {
			if len(domains) > 0 && strings.EqualFold(name, domains[0]) {
				foundName = true
				break
			}
		}
		if !foundName {
			return errors.New("证书 SAN 不包含部署主域名")
		}
	}
	if time.Now().Before(certificate.NotBefore) || time.Now().After(certificate.NotAfter) {
		return errors.New("证书不在有效期内")
	}
	keyBlock, _ := pem.Decode(bundle.PrivateKey)
	if keyBlock == nil {
		return errors.New("私钥 PEM 无效")
	}
	privateKey, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return errors.New("私钥不支持签名")
	}
	want, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return err
	}
	got, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errors.New("证书和私钥不匹配")
	}
	return validateFullChain(bundle.FullChain, certificate)
}

// validateFullChain 验证 PEM 完整性、链首身份、签名顺序、有效期与服务端证书约束。
// 链尾允许为省略根证书的中间 CA；信任来源仍是已认证控制面的叶证书指纹，不强制依赖系统根库。
func validateFullChain(content []byte, leaf *x509.Certificate) error {
	var chain []*x509.Certificate
	seen := make(map[string]bool)
	for remaining := bytes.TrimSpace(content); len(remaining) > 0; {
		if len(chain) >= 16 || !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("完整证书链包含无效内容或过多证书")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return errors.New("完整证书链 PEM 无效")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || seen[string(block.Bytes)] {
			return errors.New("完整证书链包含无效或重复证书")
		}
		seen[string(block.Bytes)] = true
		chain = append(chain, certificate)
		remaining = bytes.TrimSpace(rest)
	}
	if len(chain) == 0 || !bytes.Equal(chain[0].Raw, leaf.Raw) {
		return errors.New("完整证书链必须以当前叶证书开头")
	}
	now := time.Now()
	for index, certificate := range chain {
		if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
			return errors.New("完整证书链包含不在有效期内的证书")
		}
		if index > 0 {
			child := chain[index-1]
			if !bytes.Equal(child.RawIssuer, certificate.RawSubject) || child.CheckSignatureFrom(certificate) != nil {
				return errors.New("完整证书链的颁发者或签名关系无效")
			}
		}
	}
	if len(chain) == 1 && (!bytes.Equal(leaf.RawIssuer, leaf.RawSubject) || leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil) {
		return errors.New("完整证书链缺少叶证书的颁发者")
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(chain[len(chain)-1])
	for index := 1; index < len(chain)-1; index++ {
		intermediates.AddCert(chain[index])
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errors.New("完整证书链不满足服务端证书约束")
	}
	return nil
}

// equalDomainSets 比较两个域名列表是否表示相同的无序集合。
func equalDomainSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	// 域名规范化回调生成不受大小写和输入顺序影响的比较列表。
	canonical := func(values []string) []string {
		result := append([]string(nil), values...)
		for index := range result {
			result[index] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(result[index]), "."))
		}
		sort.Strings(result)
		return result
	}
	leftCanonical, rightCanonical := canonical(left), canonical(right)
	for index := range leftCanonical {
		if leftCanonical[index] != rightCanonical[index] {
			return false
		}
	}
	return true
}

// parsePrivateKey 兼容解析 PKCS#8、SEC1 EC 和 PKCS#1 RSA 私钥。
func parsePrivateKey(der []byte) (any, error) {
	privateKey, err := x509.ParsePKCS8PrivateKey(der)
	if err == nil {
		return privateKey, nil
	}
	if ecKey, ecErr := x509.ParseECPrivateKey(der); ecErr == nil {
		return ecKey, nil
	}
	if rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(der); rsaErr == nil {
		return rsaKey, nil
	}
	return nil, err
}
