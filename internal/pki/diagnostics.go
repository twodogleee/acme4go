// 本文件只读检查已有内部 CA，不初始化、迁移或轮换任何身份材料。
//
// @author Andersen
package pki

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

// CheckExistingCA 验证原子封装或旧双文件格式的 CA 密文、证书属性、有效期及密钥配对。
func CheckExistingCA(dataDir string, vault *cryptoutil.Vault) error {
	directory := filepath.Join(dataDir, "pki")
	content, err := os.ReadFile(filepath.Join(directory, "ca-bundle.json"))
	var certificatePEM, envelope []byte
	if err == nil {
		var bundle caBundle
		if err := json.Unmarshal(content, &bundle); err != nil || bundle.CertificatePEM == "" || bundle.EncryptedKeyEnvelope == "" {
			return errors.New("内部 CA 原子封装文件无效")
		}
		certificatePEM, envelope = []byte(bundle.CertificatePEM), []byte(bundle.EncryptedKeyEnvelope)
	} else if errors.Is(err, os.ErrNotExist) {
		certificatePEM, err = os.ReadFile(filepath.Join(directory, "ca.pem"))
		if err != nil {
			return errors.New("已有内部 CA 证书缺失或不可读")
		}
		envelope, err = os.ReadFile(filepath.Join(directory, "ca-key.pem.enc"))
		if err != nil {
			return errors.New("已有内部 CA 私钥密文缺失或不可读")
		}
	} else {
		return errors.New("内部 CA 原子封装文件不可读")
	}
	certificate, err := parseCertificate(certificatePEM)
	if err != nil {
		return errors.New("内部 CA 证书格式无效")
	}
	plaintext, err := vault.Decrypt("agent-ca-key", string(envelope))
	if err != nil {
		return errors.New("内部 CA 无法解密，请核对主密钥和备份")
	}
	signer, err := parseSigner(plaintext)
	if err != nil {
		return errors.New("内部 CA 私钥格式无效")
	}
	return validateCAKeyPair(certificate, signer, time.Now())
}
