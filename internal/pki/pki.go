// Package pki 管理 Agent 通道的内部 CA、服务端证书和节点客户端证书。
//
// @author Andersen
package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

// Manager 持有内部 CA 并按需签发短生命周期服务端和 Agent 证书。
type Manager struct {
	caCertificate  *x509.Certificate               // caCertificate 是内部 CA 证书。
	caPrivateKey   crypto.Signer                   // caPrivateKey 是解密后仅驻留内存的 CA 私钥。
	caPEM          []byte                          // caPEM 是分发给 Agent 的根证书 PEM。
	agentPublicURL string                          // agentPublicURL 是服务端证书需要覆盖的公开地址。
	rotationMu     sync.Mutex                      // rotationMu 串行化服务端证书轮换。
	serverTLS      atomic.Pointer[tls.Certificate] // serverTLS 保存可热切换的服务端证书。
}

// caBundle 将必须成对恢复的 CA 证书和加密私钥封装为一个原子文件。
type caBundle struct {
	CertificatePEM       string `json:"certificatePem"`       // CertificatePEM 是内部 CA 证书文本。
	EncryptedKeyEnvelope string `json:"encryptedKeyEnvelope"` // EncryptedKeyEnvelope 是用途绑定的 CA 私钥密文。
}

// LoadOrCreate 从加密文件加载内部 CA，不存在时生成新的独立 CA。
func LoadOrCreate(dataDir, agentPublicURL string, vault *cryptoutil.Vault) (*Manager, error) {
	directory := filepath.Join(dataDir, "pki")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	caPEM, keyEnvelope, err := loadOrCreateCABundle(directory, vault)
	if err != nil {
		return nil, err
	}
	caCertificate, err := parseCertificate(caPEM)
	if err != nil {
		return nil, err
	}
	privatePEM, err := vault.Decrypt("agent-ca-key", string(keyEnvelope))
	if err != nil {
		return nil, err
	}
	privateKey, err := parseSigner(privatePEM)
	if err != nil {
		return nil, err
	}
	if err := validateCAKeyPair(caCertificate, privateKey, time.Now().UTC()); err != nil {
		return nil, err
	}
	manager := &Manager{caCertificate: caCertificate, caPrivateKey: privateKey, caPEM: caPEM, agentPublicURL: agentPublicURL}
	serverCertificate, err := manager.issueServerCertificate(agentPublicURL)
	if err != nil {
		return nil, err
	}
	manager.serverTLS.Store(&serverCertificate)
	return manager, nil
}

// CAPEM 返回内部 CA PEM 的防御性副本。
func (m *Manager) CAPEM() []byte { return append([]byte(nil), m.caPEM...) }

// ServerTLSConfig 要求已注册请求携带内部 CA 签发的客户端证书，同时允许注册接口先建立单向 TLS。
func (m *Manager) ServerTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(m.caCertificate)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// GetCertificate 为每次 TLS 握手读取当前原子切换的服务端证书。
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificate := m.serverTLS.Load()
			if certificate == nil {
				return nil, errors.New("Agent TLS 服务端证书尚未初始化")
			}
			return certificate, nil
		},
		ClientCAs:  pool,
		ClientAuth: tls.VerifyClientCertIfGiven,
		NextProtos: []string{"h2", "http/1.1"},
	}
}

// RotateServerCertificateIfNeeded 在服务端证书进入续期窗口时生成并原子切换新证书。
func (m *Manager) RotateServerCertificateIfNeeded(now time.Time, renewBefore time.Duration) (time.Time, bool, error) {
	m.rotationMu.Lock()
	defer m.rotationMu.Unlock()
	current := m.serverTLS.Load()
	if current == nil || len(current.Certificate) == 0 {
		return time.Time{}, false, errors.New("Agent TLS 服务端证书尚未初始化")
	}
	leaf, err := x509.ParseCertificate(current.Certificate[0])
	if err != nil {
		return time.Time{}, false, err
	}
	if leaf.NotAfter.After(now.Add(renewBefore)) {
		return leaf.NotAfter, false, nil
	}
	certificate, err := m.issueServerCertificate(m.agentPublicURL)
	if err != nil {
		return leaf.NotAfter, false, err
	}
	newLeaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return leaf.NotAfter, false, err
	}
	m.serverTLS.Store(&certificate)
	return newLeaf.NotAfter, true, nil
}

// ServerCertificateExpiry 返回当前 Agent TLS 服务端证书的到期时间。
func (m *Manager) ServerCertificateExpiry() (time.Time, error) {
	current := m.serverTLS.Load()
	if current == nil || len(current.Certificate) == 0 {
		return time.Time{}, errors.New("Agent TLS 服务端证书尚未初始化")
	}
	leaf, err := x509.ParseCertificate(current.Certificate[0])
	if err != nil {
		return time.Time{}, err
	}
	return leaf.NotAfter, nil
}

// SignAgentCSR 校验 Agent 自持私钥的 CSR 并签发客户端证书。
func (m *Manager) SignAgentCSR(agentID string, csrPEM []byte) (certificatePEM []byte, serial string, expiresAt time.Time, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", time.Time{}, errors.New("Agent CSR 格式无效")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, "", time.Time{}, errors.New("Agent CSR 签名无效")
	}
	serialNumber, err := randomSerial()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	now := time.Now().UTC()
	expiresAt = now.Add(180 * 24 * time.Hour)
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: agentID, Organization: []string{"Acme4go Agent"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     expiresAt,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{{Scheme: "spiffe", Host: "acme4go", Path: "/agent/" + agentID}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, m.caCertificate, csr.PublicKey, m.caPrivateKey)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), serialNumber.Text(16), expiresAt, nil
}

// issueServerCertificate 为 Agent API 公开主机名签发短期服务端证书。
func (m *Manager) issueServerCertificate(publicURL string) (tls.Certificate, error) {
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Hostname() == "" {
		return tls.Certificate{}, errors.New("Agent 公开 URL 无效")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: parsed.Hostname(), Organization: []string{"Acme4go Control Plane"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else if !strings.EqualFold(parsed.Hostname(), "localhost") {
		template.DNSNames = append(template.DNSNames, parsed.Hostname())
	}
	der, err := x509.CreateCertificate(rand.Reader, template, m.caCertificate, &privateKey.PublicKey, m.caPrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPEM = append(certPEM, m.caPEM...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

// createCA 生成独立内部 CA，并只返回加密后的私钥信封。
func createCA(vault *cryptoutil.Vault) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Acme4go Agent CA", Organization: []string{"Acme4go"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	encrypted, err := vault.Encrypt("agent-ca-key", privatePEM)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), []byte(encrypted), nil
}

// parseCertificate 解析并返回单张 PEM 编码的内部 CA 证书。
func parseCertificate(content []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("内部 CA 证书格式无效")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseSigner 将 PKCS#8 PEM 解析为内部 CA 签名器。
func parseSigner(content []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("内部 CA 私钥格式无效")
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := value.(crypto.Signer)
	if !ok {
		return nil, errors.New("内部 CA 私钥不支持签名")
	}
	return signer, nil
}

// validateCAKeyPair 校验恢复的 CA 属性、有效期以及证书与私钥是否属于同一密钥对。
func validateCAKeyPair(certificate *x509.Certificate, signer crypto.Signer, now time.Time) error {
	if !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("内部 CA 证书缺少签发能力")
	}
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return errors.New("内部 CA 证书不在有效期内")
	}
	certificatePublicKey, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return err
	}
	signerPublicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return err
	}
	if !bytes.Equal(certificatePublicKey, signerPublicKey) {
		return errors.New("内部 CA 证书与私钥不匹配，请从同一备份恢复")
	}
	return nil
}

// randomSerial 生成 128 位密码学随机证书序列号。
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("生成证书序列号: %w", err)
	}
	return serial, nil
}

// loadOrCreateCABundle 原子加载 CA 封装，并兼容迁移旧版双文件格式。
func loadOrCreateCABundle(directory string, vault *cryptoutil.Vault) ([]byte, []byte, error) {
	bundlePath := filepath.Join(directory, "ca-bundle.json")
	content, err := os.ReadFile(bundlePath)
	if err == nil {
		var bundle caBundle
		if err := json.Unmarshal(content, &bundle); err != nil || bundle.CertificatePEM == "" || bundle.EncryptedKeyEnvelope == "" {
			return nil, nil, errors.New("内部 CA 原子封装文件无效")
		}
		return []byte(bundle.CertificatePEM), []byte(bundle.EncryptedKeyEnvelope), nil
	}
	if !os.IsNotExist(err) {
		return nil, nil, err
	}

	legacyCertificatePath := filepath.Join(directory, "ca.pem")
	legacyKeyPath := filepath.Join(directory, "ca-key.pem.enc")
	caPEM, certificateErr := os.ReadFile(legacyCertificatePath)
	keyEnvelope, keyErr := os.ReadFile(legacyKeyPath)
	certificateMissing, keyMissing := os.IsNotExist(certificateErr), os.IsNotExist(keyErr)
	switch {
	case certificateMissing && keyMissing:
		caPEM, keyEnvelope, err = createCA(vault)
		if err != nil {
			return nil, nil, err
		}
	case certificateErr == nil && keyErr == nil:
		// 兼容旧版本双文件格式，并在本次启动时迁移为原子封装。
	case certificateMissing != keyMissing:
		return nil, nil, errors.New("内部 CA 文件不完整：ca.pem 与 ca-key.pem.enc 必须同时存在，请从同一备份恢复")
	default:
		return nil, nil, errors.Join(certificateErr, keyErr)
	}
	bundleContent, err := json.Marshal(caBundle{CertificatePEM: string(caPEM), EncryptedKeyEnvelope: string(keyEnvelope)})
	if err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(bundlePath, append(bundleContent, '\n'), 0o600); err != nil {
		return nil, nil, err
	}
	return caPEM, keyEnvelope, nil
}

// writeFileAtomic 通过同步临时文件和父目录持久化 CA 原子封装。
func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".ca-bundle-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}
