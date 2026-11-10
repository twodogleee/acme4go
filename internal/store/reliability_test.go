// 本文件验证订单互斥、输入事务、历史预警、全量筛选与告警重试的回归边界。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// reliabilityStore 创建仅在临时目录中运行的数据库和系列。
func reliabilityStore(t *testing.T) (*Store, CertificateSeries, int64) {
	t.Helper()
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 测试结束关闭数据库，不触及工作区运行数据。
	t.Cleanup(func() { database.Close() })
	credential, err := database.CreateDNSCredential(context.Background(), DNSCredential{Name: "fixture", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "encrypted-id", AccessKeySecret: "encrypted-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return database, createTestSeries(t, database, credential.ID), credential.ID
}

// TestConcurrentOrderSubmissionAndTimeline 验证并发申请仅受理一次并持久化去重后的执行阶段。
func TestConcurrentOrderSubmissionAndTimeline(t *testing.T) {
	database, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	var accepted, conflicts atomic.Int32
	var orderID atomic.Int64
	var workers sync.WaitGroup
	for index := 0; index < 20; index++ {
		workers.Add(1)
		// 模拟多个管理员和调度器同时提交相同系列。
		go func() {
			defer workers.Done()
			order, err := database.CreateOrder(ctx, Order{SeriesID: series.ID, Domains: series.Domains, Email: series.Email, CAEnvironment: series.CAEnvironment, CADirectory: series.CADirectory, CredentialID: credentialID})
			if errors.Is(err, ErrActiveOrder) {
				conflicts.Add(1)
			} else if err != nil {
				t.Error(err)
			} else {
				accepted.Add(1)
				orderID.Store(order.ID)
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != 1 || conflicts.Load() != 19 {
		t.Fatalf("受理 %d，冲突 %d", accepted.Load(), conflicts.Load())
	}
	id := orderID.Load()
	if err := database.StartOrder(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := database.StartOrder(ctx, id); err == nil {
		t.Fatal("同一任务不能重复领取")
	}
	for index := 0; index < 2; index++ {
		if err := database.UpdateOrderStage(ctx, id, "AUTHORIZING_DNS"); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.FailOrder(ctx, id, "安全失败摘要"); err != nil {
		t.Fatal(err)
	}
	events, err := database.ListOrderEvents(ctx, id)
	if err != nil || len(events) != 4 || events[3].Message != "安全失败摘要" {
		t.Fatalf("时间线异常：%+v %v", events, err)
	}
	if _, err := database.CreateOrder(ctx, Order{SeriesID: series.ID, Domains: series.Domains, Email: series.Email, CAEnvironment: series.CAEnvironment, CADirectory: series.CADirectory, CredentialID: credentialID}); err != nil {
		t.Fatalf("终态释放互斥失败：%v", err)
	}
}

// TestCreateOrderTransactionRollsBackNewSeries 验证订单写入失败不会遗留新系列。
func TestCreateOrderTransactionRollsBackNewSeries(t *testing.T) {
	database, _, credentialID := reliabilityStore(t)
	ctx := context.Background()
	invalidUser := int64(99999)
	series := CertificateSeries{ID: "new-series", Code: "NEW", Name: "新系列", Domains: []string{"new.example.com"}, DomainsHash: DomainSetHash([]string{"new.example.com"}), Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CADirectory: StagingDirectory, CredentialID: credentialID}
	if _, err := database.CreateOrderWithSeries(ctx, series, &invalidUser); err == nil {
		t.Fatal("非法用户外键应阻止订单写入")
	}
	if _, err := database.FindCertificateSeries(ctx, series.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败申请遗留了系列：%v", err)
	}
}

// TestListFiltersSearchBeyondFirstPage 验证域名和状态条件在分页前作用于完整订单集。
func TestListFiltersSearchBeyondFirstPage(t *testing.T) {
	database, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	for index := 0; index < 55; index++ {
		domain := fmt.Sprintf("item-%d.example.com", index)
		order, err := database.CreateOrder(ctx, Order{SeriesID: series.ID, Domains: []string{domain}, Email: series.Email, CAEnvironment: series.CAEnvironment, CADirectory: series.CADirectory, CredentialID: credentialID})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.FailOrder(ctx, order.ID, "测试终态"); err != nil {
			t.Fatal(err)
		}
	}
	values, total, err := database.ListOrdersPage(ctx, 1, 50, ListFilter{Keyword: "ITEM-0.EXAMPLE.COM", Status: "FAILED"})
	if err != nil || total != 1 || len(values) != 1 || values[0].ID != 1 {
		t.Fatalf("跨页筛选失效：%+v %d %v", values, total, err)
	}
	_, total, err = database.ListOrdersPage(ctx, 1, 50, ListFilter{Keyword: "item-0", Status: "RUNNING"})
	if err != nil || total != 0 {
		t.Fatalf("状态总数未应用筛选：%d %v", total, err)
	}
	_, total, err = database.ListOrdersPage(ctx, 1, 50, ListFilter{Keyword: "%' OR 1=1--"})
	if err != nil || total != 0 {
		t.Fatalf("关键词不应被解释为 SQL：%d %v", total, err)
	}
}

// TestDashboardMonitorsCurrentAndActuallyDeployedVersions 验证历史过期版本仅在仍被部署使用时参与预警。
func TestDashboardMonitorsCurrentAndActuallyDeployedVersions(t *testing.T) {
	database, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	old := certificateFixture(credentialID, 100)
	old.SeriesID, old.NotAfter = series.ID, time.Now().Add(-time.Hour)
	old, err := database.InsertCertificate(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	current := certificateFixture(credentialID, 101)
	current.SeriesID, current.NotAfter = series.ID, time.Now().Add(90*24*time.Hour)
	current, err = database.InsertCertificate(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE certificate_series SET current_certificate_id=? WHERE id=?`, current.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	dashboard, err := database.Dashboard(ctx, "server", true, 30*24*time.Hour)
	if err != nil || dashboard.CertificateExpired != 0 || dashboard.CertificateTotal != 1 || dashboard.HistoryVersionTotal != 2 || len(dashboard.UpcomingCertificates) != 1 {
		t.Fatalf("历史版本污染预警：%+v %v", dashboard, err)
	}
	target, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorLocal, Name: "旧版本部署", CertificateSeriesID: &series.ID, CommonName: "example.com", TargetAlias: "old", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// 构造历史时点已成功部署、如今过期的记录，不能通过当前部署接口接受过期证书。
	if _, err := database.db.ExecContext(ctx, `INSERT INTO deployment_records(target_id,executor_type,certificate_id,fingerprint,status,message,created_at) VALUES(?,'LOCAL',?,?,'SUCCESS','旧版已部署',?)`, target.ID, old.ID, old.Fingerprint, nowText()); err != nil {
		t.Fatal(err)
	}
	dashboard, err = database.Dashboard(ctx, "server", true, 30*24*time.Hour)
	if err != nil || dashboard.CertificateExpired != 1 || dashboard.UpcomingCertificates[0].ID != old.ID {
		t.Fatalf("仍在部署的旧版本必须预警：%+v %v", dashboard, err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, target.Revision, current.ID, current.Fingerprint, "FAILED", "新版部署失败"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE deployment_records SET created_at=?`, timeText(time.Now().Add(-365*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := database.PruneDeploymentRecords(ctx, time.Now().Add(-24*time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	records, err := database.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 1 || records[0].Status != "SUCCESS" || records[0].CertificateID == nil || *records[0].CertificateID != old.ID {
		t.Fatalf("清理必须保留实际部署版本依据：%+v %v", records, err)
	}
	dashboard, err = database.Dashboard(ctx, "server", true, 30*24*time.Hour)
	if err != nil || dashboard.CertificateExpired != 1 {
		t.Fatalf("历史清理不应丢失仍在使用的过期版本：%+v %v", dashboard, err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, target.Revision, current.ID, current.Fingerprint, "SUCCESS", "当前版已部署"); err != nil {
		t.Fatal(err)
	}
	dashboard, err = database.Dashboard(ctx, "server", true, 30*24*time.Hour)
	if err != nil || dashboard.CertificateExpired != 0 {
		t.Fatalf("切换成功后旧版应退出预警：%+v %v", dashboard, err)
	}
	values, total, err := database.ListCertificatesPage(ctx, 1, 1, 1, ListFilter{Status: "EXPIRED"})
	if err != nil || total != 1 || len(values) != 1 {
		t.Fatalf("历史库仍应支持查询过期版本：%v %d %v", values, total, err)
	}
	summary, err := database.CertificateSummary(ctx, 30*24*time.Hour)
	if err != nil || summary.Total != 2 || summary.Expired != 1 || summary.Valid != 1 {
		t.Fatalf("完整版本统计错误：%+v %v", summary, err)
	}
}

// TestAlertQueueRetryLeaseAndDeduplication 验证去重、租约恢复、重试上限和人工重新排队。
func TestAlertQueueRetryLeaseAndDeduplication(t *testing.T) {
	database, _, _ := reliabilityStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Second)
	for index := 0; index < 2; index++ {
		if err := database.EnqueueAlert(ctx, AlertEvent{Event: "test", Severity: "ERROR", Message: "安全消息"}, "one-event"); err != nil {
			t.Fatal(err)
		}
	}
	values, total, err := database.ListAlertDeliveries(ctx, 1, 50, "ALL")
	if err != nil || total != 1 || len(values) != 1 {
		t.Fatalf("事件去重错误：%d %v", total, err)
	}
	first, err := database.ClaimAlert(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimAlert(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("有效租约不能再次认领")
	}
	current, err := database.ClaimAlert(ctx, now.Add(61*time.Second))
	if err != nil || current.Attempts != 2 {
		t.Fatalf("过期租约无法恢复：%+v %v", current, err)
	}
	if err := database.FinishAlert(ctx, first, "", now); err != nil {
		t.Fatal(err)
	}
	values, _, _ = database.ListAlertDeliveries(ctx, 1, 50, "SENDING")
	if len(values) != 1 {
		t.Fatal("旧租约结果覆盖了新认领")
	}
	for current.Attempts <= 8 {
		if err := database.FinishAlert(ctx, current, "HTTP 503", now); err != nil {
			t.Fatal(err)
		}
		if current.Attempts == 8 {
			break
		}
		now = now.Add(time.Hour)
		current, err = database.ClaimAlert(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	values, total, err = database.ListAlertDeliveries(ctx, 1, 50, "FAILED")
	if err != nil || total != 1 || values[0].Attempts != 8 {
		t.Fatalf("重试上限失效：%+v %v", values, err)
	}
	if err := database.RetryAlert(ctx, current.ID); err != nil {
		t.Fatal(err)
	}
	current, err = database.ClaimAlert(ctx, now.Add(time.Hour))
	if err != nil || current.Attempts != 1 {
		t.Fatalf("人工重新排队未重置次数：%+v %v", current, err)
	}
	if err := database.FinishAlert(ctx, current, "", now); err != nil {
		t.Fatal(err)
	}
	if err := database.RetryAlert(ctx, current.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("已送达事件不应被重复发送")
	}
}

// TestLegacyDNSAndDuplicateOrdersMigration 验证旧库凭据约束、活动订单与所有主键引用可安全升级并重复启动。
func TestLegacyDNSAndDuplicateOrdersMigration(t *testing.T) {
	directory := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(directory, "acme4go.db"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(schemaSQL, "provider IN ('ALIYUN','CLOUDFLARE')", "provider = 'ALIYUN'", 1)
	if _, err := raw.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO dns_credentials(id,name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,created_at,updated_at) VALUES(7,'旧凭据','ALIYUN','example.com','原密文标识','原密文秘密',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO certificate_series(id,code,name,domains_json,domains_hash,email,ca_environment,ca_directory,credential_id,created_at,updated_at) VALUES('legacy','LEGACY','旧系列','["example.com"]','hash','ops@example.com','LETS_ENCRYPT_STAGING',?,7,?,?)`, StagingDirectory, nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if _, err := raw.Exec(`INSERT INTO certificate_orders(series_id,domains_json,email,ca_environment,ca_directory,credential_id,status,stage,created_at,updated_at) VALUES('legacy','["example.com"]','ops@example.com','LETS_ENCRYPT_STAGING',?,7,'QUEUED','QUEUED',?,?)`, StagingDirectory, nowText(), nowText()); err != nil {
			t.Fatal(err)
		}
	}
	// 旧库已经完成前三个迁移，只执行此次新增的版本。
	for index, name := range []string{"certificate_series", "certificate_series_version_constraint", "operational_indexes"} {
		if _, err := raw.Exec(`INSERT INTO schema_migrations VALUES(?,?,?)`, index+1, name, nowText()); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		database, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		credential, err := database.FindDNSCredential(context.Background(), 7)
		if err != nil || credential.AccessKeySecret != "原密文秘密" {
			t.Fatalf("迁移改变凭据：%+v %v", credential, err)
		}
		orders, total, err := database.ListOrdersPage(context.Background(), 1, 50)
		if err != nil || total != 2 || orders[0].Status != "FAILED" || orders[1].Status != "QUEUED" {
			t.Fatalf("重复订单未安全终结：%+v %v", orders, err)
		}
		if _, err := database.CreateDNSCredential(context.Background(), DNSCredential{Name: "Cloudflare", Provider: "CLOUDFLARE", Zone: "example.net", AccessKeyID: "encrypted", AccessKeySecret: "encrypted", Enabled: true}); err != nil {
			t.Fatal(err)
		}
		var foreignKeys int
		if err := database.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatal("迁移后外键没有恢复")
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
