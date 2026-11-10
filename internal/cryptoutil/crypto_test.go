// 本文件验证密文用途绑定和密码摘要往返行为。
//
// @author Andersen
package cryptoutil

import "testing"

// TestMalformedNonceReturnsError 验证损坏密文被诊断为错误，不触发 GCM 的长度 Panic。
func TestMalformedNonceReturnsError(t *testing.T) {
	vault, err := NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Decrypt("diagnostic", "v1.AA.AA"); err == nil {
		t.Fatal("错误 Nonce 长度未被拒绝")
	}
}

// TestVaultPurposeBinding 验证不同业务用途不能互相解密密文。
func TestVaultPurposeBinding(t *testing.T) {
	vault, err := NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := vault.Encrypt("dns-secret", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := vault.Decrypt("dns-secret", envelope)
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("解密结果异常: %q, %v", plaintext, err)
	}
	if _, err := vault.Decrypt("certificate-key", envelope); err == nil {
		t.Fatal("不同用途不应能够解密同一密文")
	}
}

// TestPasswordRoundTrip 验证正确密码通过而错误密码被拒绝。
func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("正确密码未通过验证")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("错误密码通过了验证")
	}
}
