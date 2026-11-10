// 本文件验证凭据跨用途共享、秘密更新、用途隔离和删除保护。
//
// @author Andersen
package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSharedCredentialScopesAndRotation 验证两个用途读取同一秘密，轮换及单用途停用立即生效。
func TestSharedCredentialScopesAndRotation(t *testing.T) {
	s, _, _, _, _ := cloudFixture(t)
	ctx := context.Background()
	input := CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "共享授权", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "shared-aliyun-id", AccessKeySecret: "shared-original-secret", Enabled: true}, DNSEnabled: true, CloudEnabled: true}
	value, err := s.SaveCredential(ctx, 1, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if !value.AccessKeyIDLocked {
		t.Fatal("云身份未锁定")
	}
	credentialID := value.ID
	dns, err := s.DecryptedDNSCredential(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := s.cloudCredential(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if dns.AccessKeySecret != input.AccessKeySecret || cloud.AccessKeySecret != dns.AccessKeySecret {
		t.Fatal("用途没有共享秘密")
	}
	input.AccessKeyID = ""
	input.AccessKeySecret = "shared-rotated-secret"
	value, err = s.SaveCredential(ctx, 1, value.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if value.ID != credentialID {
		t.Fatal("更新改变了云资源引用")
	}
	dns, err = s.DecryptedDNSCredential(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	cloud, err = s.cloudCredential(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if dns.AccessKeySecret != "shared-rotated-secret" || cloud.AccessKeySecret != dns.AccessKeySecret {
		t.Fatal("密钥更新未被两个用途读取")
	}
	values, err := s.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(values)
	if strings.Contains(string(raw), "shared-rotated-secret") || strings.Contains(string(raw), "cloudIdentityHash") {
		t.Fatal("共享列表泄露秘密")
	}
	input.DNSEnabled = false
	input.AccessKeySecret = ""
	if _, err = s.SaveCredential(ctx, 1, value.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecryptedDNSCredential(ctx, value.ID); err == nil {
		t.Fatal("未启用 DNS 用途仍可签发")
	}
	if _, err = s.cloudCredential(ctx, credentialID); err != nil {
		t.Fatal("关闭 DNS 同时禁用了云部署")
	}
	input.DNSEnabled = true
	input.CloudEnabled = false
	if _, err = s.SaveCredential(ctx, 1, value.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.cloudCredential(ctx, credentialID); err == nil {
		t.Fatal("关闭云用途未生效")
	}
	if _, err = s.DecryptedDNSCredential(ctx, value.ID); err != nil {
		t.Fatal("关闭云用途破坏了 DNS")
	}
	input.AccessKeyID = "another-account"
	if _, err = s.SaveCredential(ctx, 1, value.ID, input); err == nil {
		t.Fatal("关闭云用途后仍不应替换已绑定的云身份")
	}
}

// TestSharedCredentialRejectsUnsupportedAndReferencedDeletion 验证服务商用途边界和云资源引用保护。
func TestSharedCredentialRejectsUnsupportedAndReferencedDeletion(t *testing.T) {
	s, account, cert, _, _ := cloudFixture(t)
	ctx := context.Background()
	if _, err := s.SaveCredential(ctx, 1, 0, CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "Cloudflare", Provider: "CLOUDFLARE", AccessKeySecret: "token", Enabled: true}, CloudEnabled: true}); err == nil {
		t.Fatal("Cloudflare 被误授权云部署")
	}
	if _, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "已引用", CredentialID: account.ID, Kind: "UPLOAD", SeriesID: &cert.SeriesID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCredential(ctx, 1, account.ID); err == nil {
		t.Fatal("仍被云规则引用的凭据被删除")
	}
	if _, err := s.cloudCredential(ctx, account.ID); err != nil {
		t.Fatal("删除失败未完整回滚")
	}
	before, err := s.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCredential(ctx, 1, 0, CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "重复云身份", Provider: "ALIYUN", AccessKeyID: "test-cloud-id", AccessKeySecret: "test-cloud-secret", Enabled: true}, CloudEnabled: true}); err == nil {
		t.Fatal("同一云身份被重复登记")
	}
	after, err := s.ListCredentials(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatal("失败创建留下了多余秘密副本")
	}
}

// TestLegacyDNSUpdateSharesCloudSecret 验证旧 DNS 接口也进入共享修改及活动任务保护。
func TestLegacyDNSUpdateSharesCloudSecret(t *testing.T) {
	s, account, cert, _, _ := cloudFixture(t)
	ctx := context.Background()
	input := DNSCredentialInput{Name: "共享", Provider: "ALIYUN", Zone: "example.com", AccessKeySecret: "new-via-dns", Enabled: true}
	if _, err := s.UpdateDNSCredential(ctx, 1, account.ID, input); err != nil {
		t.Fatal(err)
	}
	cloud, err := s.cloudCredential(ctx, account.ID)
	if err != nil || cloud.AccessKeySecret != "new-via-dns" {
		t.Fatal("旧接口未更新共享秘密")
	}
	plan, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "活动", CredentialID: account.ID, Kind: "UPLOAD", SeriesID: &cert.SeriesID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueCloudPlan(ctx, 1, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateDNSCredential(ctx, 1, account.ID, input); err == nil {
		t.Fatal("旧入口绕过活动云任务保护")
	}
}
