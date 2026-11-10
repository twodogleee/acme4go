// Package cryptoutil 提供密钥派生、敏感数据封装和密码摘要能力。
//
// @author Andersen
package cryptoutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

// passwordFormat 固定当前 Argon2id 密码摘要版本和资源参数。
const passwordFormat = "argon2id$v=19$m=19456,t=2,p=1"

const (
	// MaxUsernameBytes 限制认证用户名的 UTF-8 字节数，初始化与登录使用相同边界。
	MaxUsernameBytes = 256
	// MaxPasswordBytes 限制密码输入字节数，避免高成本认证接收无限长输入。
	MaxPasswordBytes = 1024
)

// Vault 使用主密钥派生不同用途的 AES-256-GCM 密钥，避免跨用途复用密钥材料。
type Vault struct {
	master []byte // master 是复制保存且不直接用于业务加密的主密钥。
}

// NewVault 创建敏感数据加密器。
func NewVault(master []byte) (*Vault, error) {
	if len(master) < 32 {
		return nil, errors.New("主密钥至少需要 32 字节")
	}
	return &Vault{master: append([]byte(nil), master...)}, nil
}

// Encrypt 使用随机 Nonce 加密明文，返回带格式版本的文本封装。
func (v *Vault) Encrypt(purpose string, plaintext []byte) (string, error) {
	key, err := v.derive(purpose)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(purpose))
	return "v1." + base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 验证用途绑定和 GCM 标签后解密文本封装。
func (v *Vault) Decrypt(purpose, envelope string) ([]byte, error) {
	parts := strings.Split(envelope, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, errors.New("不支持的密文格式")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("密文 Nonce 无效")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("密文正文无效")
	}
	key, err := v.derive(purpose)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("密文 Nonce 长度无效")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return nil, errors.New("密文认证失败")
	}
	return plaintext, nil
}

// derive 通过 HKDF 为每个业务用途派生独立的 AES-256 密钥。
func (v *Vault) derive(purpose string) ([]byte, error) {
	reader := hkdf.New(sha256.New, v.master, []byte("acme4go-v1"), []byte(purpose))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("派生加密密钥: %w", err)
	}
	return key, nil
}

// HashPassword 使用 Argon2id 和独立随机盐生成自描述密码摘要。
func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", errors.New("密码不能超过 1024 字节")
	}
	if len(password) < 12 {
		return "", errors.New("密码至少需要 12 位")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return passwordFormat + "$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}

// VerifyPassword 以常量时间比较 Argon2id 摘要。
func VerifyPassword(encoded, password string) bool {
	if len(password) > MaxPasswordBytes {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return false
	}
	params := strings.Split(parts[2], ",")
	if len(params) != 3 {
		return false
	}
	memory, okM := parseParam(params[0], "m=")
	timeCost, okT := parseParam(params[1], "t=")
	parallelism, okP := parseParam(params[2], "p=")
	if !okM || !okT || !okP || memory > 256*1024 || timeCost > 10 || parallelism > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) < 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(timeCost), uint32(memory), uint8(parallelism), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// RandomToken 生成指定随机字节数的 URL 安全文本。
func RandomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// TokenHash 返回不保存原始令牌的 SHA-256 索引。
func TokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// parseParam 解析密码摘要中的正整数参数并限制为 32 位范围。
func parseParam(value, prefix string) (uint64, bool) {
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 32)
	return parsed, err == nil && parsed > 0
}
