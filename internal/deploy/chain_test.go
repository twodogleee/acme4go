// 本文件验证证书链格式、签名顺序、有效期与磁盘损坏检测。
//
// @author Andersen
package deploy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// chainCertificate 创建合成 CA 或服务器证书，可指定真实颁发者和到期时间。
func chainCertificate(t *testing.T, name string, ca bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, expires time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: expires, BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature}
	if ca {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestFullChainValidationAndDiskCorruption 验证完整链和省略根的常见链均可用，损坏链不能被部署校验掩盖。
func TestFullChainValidationAndDiskCorruption(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	root, rootKey, rootPEM := chainCertificate(t, "root", true, nil, nil, expires)
	intermediate, intermediateKey, intermediatePEM := chainCertificate(t, "intermediate", true, root, rootKey, expires)
	leaf, key, leafPEM := chainCertificate(t, "example.com", false, intermediate, intermediateKey, expires)
	_, _, unrelatedPEM := chainCertificate(t, "unrelated", true, nil, nil, expires)
	// 保持同一颁发者和公钥，仅改变有效期，确保过期用例不被其他签名错误提前拒绝。
	expiredTemplate := *intermediate
	expiredTemplate.NotAfter = time.Now().Add(-time.Hour)
	expiredDER, err := x509.CreateCertificate(rand.Reader, &expiredTemplate, root, intermediate.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	expiredIntermediatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: expiredDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(leaf.Raw)
	bundle := Bundle{Certificate: leafPEM, PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), CommonName: "example.com", SANs: []string{"example.com"}, Fingerprint: hex.EncodeToString(digest[:])}
	// 链拼接回调避免 append 复用输入容量污染其他测试材料。
	join := func(parts ...[]byte) []byte {
		var result []byte
		for _, part := range parts {
			result = append(result, part...)
		}
		return result
	}
	for _, chain := range [][]byte{join(leafPEM, intermediatePEM), join(leafPEM, intermediatePEM, rootPEM)} {
		bundle.FullChain = chain
		if err := ValidateBundleDomains(bundle, bundle.SANs, bundle.Fingerprint, true); err != nil {
			t.Fatalf("有效链被拒绝：%v", err)
		}
	}
	for name, chain := range map[string][]byte{
		"empty":            nil,
		"garbage":          []byte("not a certificate chain"),
		"wrong-leaf":       join(intermediatePEM, rootPEM),
		"missing-issuer":   leafPEM,
		"reordered":        join(leafPEM, rootPEM, intermediatePEM),
		"unrelated":        join(leafPEM, unrelatedPEM),
		"duplicate":        join(leafPEM, intermediatePEM, intermediatePEM),
		"trailing-garbage": join(leafPEM, intermediatePEM, []byte("broken")),
		"expired":          join(leafPEM, expiredIntermediatePEM, rootPEM),
	} {
		bundle.FullChain = chain
		if err := ValidateBundleDomains(bundle, bundle.SANs, bundle.Fingerprint, true); err == nil {
			t.Errorf("接受损坏链：%s", name)
		}
	}
	bundle.FullChain = join(leafPEM, intermediatePEM)
	directory := t.TempDir()
	current, err := Materialize(directory, "chain", 1, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "fullchain.pem"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMaterialized(directory, "chain", bundle.SANs, bundle.Fingerprint); err == nil {
		t.Fatal("磁盘链损坏没有触发重新部署")
	}
}
