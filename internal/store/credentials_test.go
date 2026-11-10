// 本文件验证统一凭据升级保留已有 DNS 密文、用途默认值和证书系列引用。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestUnifiedCredentialsPreservesExistingDNS 验证从未启用云部署的数据库升级时不改写 DNS 数据。
func TestUnifiedCredentialsPreservesExistingDNS(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, deploymentGate: make(chan struct{}, 1)}
	ctx := context.Background()
	original, err := s.CreateDNSCredential(ctx, DNSCredential{Name: "已有 DNS", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "existing-id-envelope", AccessKeySecret: "existing-secret-envelope", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, s, original.ID)
	if err := s.runMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.migrateUnifiedCredentials(ctx); err != nil {
			t.Fatal(err)
		}
	}
	values, err := s.ListCredentials(ctx)
	if err != nil || len(values) != 1 {
		t.Fatalf("升级改变了凭据数量: %d %v", len(values), err)
	}
	value := values[0]
	if value.ID != original.ID || value.AccessKeyID != original.AccessKeyID || value.AccessKeySecret != original.AccessKeySecret || value.Zone != original.Zone || !value.Enabled || !value.DNSEnabled || value.CloudEnabled || value.AccessKeyIDLocked {
		t.Fatal("已有 DNS 密文、主键或用途被修改")
	}
	current, err := s.FindCertificateSeries(ctx, series.ID, 30*24*time.Hour)
	if err != nil || current.CredentialID != original.ID {
		t.Fatal("已有证书系列引用被破坏")
	}
	var separateAccounts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='cloud_accounts'`).Scan(&separateAccounts); err != nil || separateAccounts != 0 {
		t.Fatal("不应创建独立云账号表")
	}
}
