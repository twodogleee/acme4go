// 本文件验证多根域名凭据迁移、范围约束和测试结果存储。
//
// @author Andersen
package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestCredentialZonesUpgradeAndPreserve 验证旧库升级、重复迁移、秘密与引用不变，以及新数组不被重跑覆盖。
func TestCredentialZonesUpgradeAndPreserve(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v, err := s.CreateDNSCredential(ctx, DNSCredential{Name: "旧凭据", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "encrypted-id", AccessKeySecret: "encrypted-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, s, v.ID)
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version=11; ALTER TABLE dns_credentials DROP COLUMN zones_json; ALTER TABLE dns_credentials DROP COLUMN last_test_results_json`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err = s.FindCredential(ctx, v.ID)
	if err != nil || !reflect.DeepEqual(v.Zones, []string{"example.com"}) || v.AccessKeySecret != "encrypted-secret" {
		t.Fatal("升级损坏旧凭据", err)
	}
	current, err := s.FindCertificateSeries(ctx, series.ID, time.Hour)
	if err != nil || current.CredentialID != v.ID {
		t.Fatal("升级损坏系列引用", err)
	}
	v.Zones = []string{"example.com", "example.net"}
	if _, err := s.SaveCredential(ctx, v, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.migrateCredentialZones(ctx); err != nil {
			t.Fatal(err)
		}
	}
	v, err = s.FindCredential(ctx, v.ID)
	if err != nil || len(v.Zones) != 2 {
		t.Fatal("重跑迁移覆盖多域名", err)
	}
	v.Zones = []string{"example.net"}
	var scopeErr *CredentialScopeError
	if _, err := s.SaveCredential(ctx, v, ""); !errors.As(err, &scopeErr) {
		t.Fatalf("移除关联根域名应被拒绝: %v", err)
	}
	v, _ = s.FindCredential(ctx, v.ID)
	if len(v.Zones) != 2 {
		t.Fatal("失败保存没有回滚")
	}
}

// TestCredentialZoneSnapshotsAndTestResults 验证第二根域名变更也阻止过期订单受理，旧测试不覆盖新配置。
func TestCredentialZoneSnapshotsAndTestResults(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	v, err := s.CreateDNSCredential(ctx, DNSCredential{Name: "多域名", Provider: "ALIYUN", Zones: []string{"example.com", "example.net"}, AccessKeyID: "encrypted-id", AccessKeySecret: "encrypted-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, s, v.ID)
	old := v
	v.Zones = []string{"example.com", "example.org"}
	if _, err := s.SaveCredential(ctx, v, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOrderWithSeries(ctx, series, nil, old.Zones...); !errors.Is(err, ErrSeriesChanged) {
		t.Fatalf("未发现第二域名发生变化: %v", err)
	}
	if err := s.UpdateCertificateSeries(ctx, series, old.Zones...); !errors.Is(err, ErrSeriesChanged) {
		t.Fatalf("系列换绑未检查范围快照: %v", err)
	}
	results := []DNSZoneTestResult{{Zone: "example.com", Success: true, Message: "成功", TestedAt: time.Now().UTC()}, {Zone: "example.org", Success: false, Message: "失败", TestedAt: time.Now().UTC()}}
	if err := s.RecordDNSZoneTests(ctx, old, results); !errors.Is(err, ErrSeriesChanged) {
		t.Fatal("旧测试覆盖新配置", err)
	}
	v, _ = s.FindCredential(ctx, v.ID)
	if err := s.RecordDNSZoneTests(ctx, v, results); err != nil {
		t.Fatal(err)
	}
	v, _ = s.FindCredential(ctx, v.ID)
	if v.LastTestStatus == nil || *v.LastTestStatus != "FAILED" || len(v.LastTestResults) != 2 || !v.LastTestResults[0].Success {
		t.Fatal("逐域名结果未持久化")
	}
	if _, err := s.SaveCredential(ctx, v, ""); err != nil {
		t.Fatal(err)
	}
	v, _ = s.FindCredential(ctx, v.ID)
	if len(v.LastTestResults) != 0 || v.LastTestStatus != nil {
		t.Fatal("修改后未清理过期测试结果")
	}
}

// TestCredentialScopeProtectsOldActiveOrder 验证系列换绑后，旧凭据仍保留运行中申请需要的根域名。
func TestCredentialScopeProtectsOldActiveOrder(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	v, err := s.CreateDNSCredential(ctx, DNSCredential{Name: "原凭据", Provider: "ALIYUN", Zones: []string{"example.com", "example.net"}, AccessKeyID: "encrypted-id", AccessKeySecret: "encrypted-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, s, v.ID)
	order, err := s.CreateOrderWithSeries(ctx, series, nil, v.Zones...)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.CreateDNSCredential(ctx, DNSCredential{Name: "新凭据", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "encrypted-id-2", AccessKeySecret: "encrypted-secret-2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series.CredentialID = replacement.ID
	if err := s.UpdateCertificateSeries(ctx, series, replacement.Zones...); err != nil {
		t.Fatal(err)
	}
	v.Zones = []string{"example.net"}
	var scopeErr *CredentialScopeError
	if _, err := s.SaveCredential(ctx, v, ""); !errors.As(err, &scopeErr) {
		t.Fatal("活动申请所需域名被移除", err)
	}
	if err := s.FailOrder(ctx, order.ID, "测试结束"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCredential(ctx, v, ""); err != nil {
		t.Fatal("引用迁移且申请结束后应允许移除", err)
	}
}
