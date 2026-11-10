// 本文件复现已登记迁移但云表仍使用旧字段的升级路径，验证修复不丢失业务数据。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyCloudDatabase 创建已执行旧版云建表迁移的临时数据库，保留真实 DNS 系列引用。
func legacyCloudDatabase(t *testing.T, version int) (string, DNSCredential, CertificateSeries) {
	t.Helper()
	directory := t.TempDir()
	s, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	credential, err := s.CreateDNSCredential(ctx, DNSCredential{Name: "原有 DNS", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "dns-id-envelope", AccessKeySecret: "dns-secret-envelope", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, s, credential.ID)
	// 使用旧云账号外键构造旧表；仅修改测试临时库，不读取运行环境数据。
	legacySchema := `DROP TABLE cloud_jobs; DROP TABLE cloud_uploads; DROP TABLE cloud_plans;
CREATE TABLE cloud_accounts(id INTEGER PRIMARY KEY, name TEXT NOT NULL, access_key_id_enc TEXT NOT NULL, access_key_secret_enc TEXT NOT NULL, identity_hash TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1);` + strings.ReplaceAll(strings.ReplaceAll(cloudSchema, "credential_id", "account_id"), "REFERENCES dns_credentials(id)", "REFERENCES cloud_accounts(id)")
	if _, err := s.db.ExecContext(ctx, legacySchema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>?`, version); err != nil {
		t.Fatal(err)
	}
	// 初版版本 9 有加密上下文列但没有当前的云身份摘要列；已执行记录会跳过版本 9。
	if _, err := s.db.ExecContext(ctx, `DROP INDEX idx_credential_cloud_identity; ALTER TABLE dns_credentials DROP COLUMN cloud_identity_hash; ALTER TABLE dns_credentials ADD COLUMN secret_context TEXT NOT NULL DEFAULT 'dns'`); err != nil {
		t.Fatal(err)
	}
	if version < 9 {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE dns_credentials DROP COLUMN dns_enabled; ALTER TABLE dns_credentials DROP COLUMN cloud_enabled`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ListCloudPlans(ctx); err == nil || !strings.Contains(err.Error(), "credential_id") {
		t.Fatalf("未复现旧表字段不匹配: %v", err)
	}
	return directory, credential, series
}

// TestUpgradeUnusedLegacyCloudSchema 验证旧版本 7、8、9 的空云表均可升级并正常执行新云任务。
func TestUpgradeUnusedLegacyCloudSchema(t *testing.T) {
	for _, version := range []int{7, 8, 9} {
		// 每个旧版本均通过真实 Open 路径检查迁移记录跳过后的结构修复。
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			directory, original, series := legacyCloudDatabase(t, version)
			s, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			if values, err := s.ListCloudPlans(ctx); err != nil || len(values) != 0 {
				t.Fatalf("修复后仍无法扫描空云表: %v", err)
			}
			credential, err := s.FindCredential(ctx, original.ID)
			if err != nil || credential.Name != original.Name || credential.Zone != original.Zone || credential.AccessKeyID != original.AccessKeyID || credential.AccessKeySecret != original.AccessKeySecret || !credential.DNSEnabled || credential.CloudEnabled || credential.AccessKeyIDLocked {
				t.Fatal("已有 DNS 凭据被改变")
			}
			current, err := s.FindCertificateSeries(ctx, series.ID, 30*24*time.Hour)
			if err != nil || current.CredentialID != original.ID {
				t.Fatal("证书系列引用被改变")
			}
			credential.CloudEnabled = true
			if _, err := s.SaveCredential(ctx, credential, "cloud-identity"); err != nil {
				t.Fatal(err)
			}
			planID, err := s.SaveCloudPlan(ctx, CloudPlan{Name: "新部署目标", CredentialID: original.ID, Kind: "DEPLOY", Product: "CDN", RemoteCertificateID: "123", Config: []byte(`{"domain":"example.com"}`), Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := s.FindCloudPlan(ctx, planID)
			if err != nil || plan.CredentialID != original.ID {
				t.Fatalf("云配置引用无效: %v", err)
			}
			if err := s.SaveCloudUpload(ctx, original.ID, "fingerprint", "123"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnqueueCloudJob(ctx, plan, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteCredential(ctx, original.ID); err == nil {
				t.Fatal("修复后的外键未保护已有引用")
			}
			for _, table := range []string{"cloud_plans", "cloud_uploads"} {
				var references int
				if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list(?) WHERE "table"='dns_credentials' AND "from"='credential_id'`, table).Scan(&references); err != nil || references != 1 {
					t.Fatalf("%s 未直接引用统一凭据: %v", table, err)
				}
			}
			// 模拟结构修复已提交但迁移记录尚未落盘，重试必须保留新模型的非空数据。
			if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=10`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if values, err := s.ListCloudPlans(ctx); err != nil || len(values) != 1 || values[0].ID != planID {
				t.Fatal("重复启动丢失新云配置")
			}
			if id, err := s.FindCloudUpload(ctx, original.ID, "fingerprint"); err != nil || id != "123" {
				t.Fatal("重复启动丢失上传映射")
			}
			if job, err := s.NextCloudJob(ctx); err != nil || job.PlanID != planID || job.Status != "QUEUED" {
				t.Fatal("重复启动丢失任务")
			}
		})
	}
}

// TestUpgradeLegacyCloudDataIsNotDeleted 验证已有旧云账号时保留数据并明确拒绝自动清空。
func TestUpgradeLegacyCloudDataIsNotDeleted(t *testing.T) {
	directory, _, _ := legacyCloudDatabase(t, 9)
	db, err := sql.Open("sqlite", filepath.Join(directory, "acme4go.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO cloud_accounts(id,name,access_key_id_enc,access_key_secret_enc,identity_hash) VALUES(1,'试用账号','old-id-envelope','old-secret-envelope','old-identity')`); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(directory)
	if err == nil {
		upgraded.Close()
		t.Fatal("存在旧云数据时不应默默清空或带错误结构启动")
	}
	if !strings.Contains(err.Error(), "旧版云部署数据") {
		t.Fatalf("缺少可定位的升级错误: %v", err)
	}
	var secret string
	if err := db.QueryRow(`SELECT access_key_secret_enc FROM cloud_accounts WHERE id=1`).Scan(&secret); err != nil || secret != "old-secret-envelope" {
		t.Fatal("拒绝升级时丢失了已有云密文")
	}
	var recorded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=10`).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatal("失败修复被错误登记为已完成")
	}
}
