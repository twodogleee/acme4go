// 本文件诊断已有 DNS 与 ACME 账户密文，不输出明文或修改账户材料。
//
// @author Andersen
package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/store"
)

// checkCredentialEnvelopes 检查全部 DNS 密文和 ACME 账户私钥；停用凭据也属于备份完整性边界。
func checkCredentialEnvelopes(ctx context.Context, database *store.Store, vault *cryptoutil.Vault) error {
	credentials, err := database.ListDNSCredentials(ctx)
	if err != nil {
		return err
	}
	for _, credential := range credentials {
		if _, err := vault.Decrypt("dns-access-key-id", credential.AccessKeyID); err != nil {
			return fmt.Errorf("DNS 凭据 #%d 标识密文不可解密", credential.ID)
		}
		if secret, err := vault.Decrypt("dns-access-key-secret", credential.AccessKeySecret); err != nil || len(secret) == 0 {
			return fmt.Errorf("DNS 凭据 #%d 秘密密文不可解密或内容为空", credential.ID)
		}
	}
	accounts, err := database.ListACMEAccounts(ctx)
	if err != nil {
		return err
	}
	for index, account := range accounts {
		content, err := vault.Decrypt("acme-account:"+account.Key, account.PrivateKey)
		if err != nil {
			return fmt.Errorf("ACME 账户 #%d 密文不可解密", index+1)
		}
		block, _ := pem.Decode(content)
		if block == nil || block.Type != "PRIVATE KEY" {
			return fmt.Errorf("ACME 账户 #%d 私钥 PEM 无效", index+1)
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("ACME 账户 #%d 私钥格式无效", index+1)
		}
		if _, ok := key.(crypto.Signer); !ok {
			return fmt.Errorf("ACME 账户 #%d 私钥不能签名", index+1)
		}
	}
	return nil
}
