// 本文件验证 SQLite 仓库的迁移、并发、状态聚合和 Agent 授权边界。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestOpenMigratesLegacySchemaColumns 验证旧数据库缺少系列列时可原地升级并归属历史证书。
func TestOpenMigratesLegacySchemaColumns(t *testing.T) {
	directory := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(directory, "acme4go.db"))
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
		CREATE TABLE dns_credentials (id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,provider TEXT NOT NULL,zone TEXT NOT NULL,access_key_id_enc TEXT NOT NULL,access_key_secret_enc TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,last_test_status TEXT,last_test_message TEXT,last_test_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
		CREATE TABLE certificates (id INTEGER PRIMARY KEY AUTOINCREMENT,common_name TEXT NOT NULL,sans_json TEXT NOT NULL,issuer TEXT NOT NULL,serial_number TEXT NOT NULL,not_before TEXT NOT NULL,not_after TEXT NOT NULL,fingerprint TEXT NOT NULL UNIQUE,version INTEGER NOT NULL,cert_path TEXT NOT NULL,chain_path TEXT NOT NULL,key_path TEXT NOT NULL,email TEXT NOT NULL,ca_directory TEXT NOT NULL,credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
		CREATE TABLE certificate_orders (id INTEGER PRIMARY KEY AUTOINCREMENT,domains_json TEXT NOT NULL,email TEXT NOT NULL,ca_environment TEXT NOT NULL,ca_directory TEXT NOT NULL,credential_id INTEGER NOT NULL REFERENCES dns_credentials(id),status TEXT NOT NULL,stage TEXT NOT NULL,error_message TEXT,certificate_id INTEGER REFERENCES certificates(id),requested_by INTEGER,started_at TEXT,completed_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
		CREATE TABLE deployment_targets (id INTEGER PRIMARY KEY AUTOINCREMENT,executor_type TEXT NOT NULL,agent_id TEXT,name TEXT NOT NULL,common_name TEXT NOT NULL,target_alias TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,revision INTEGER NOT NULL DEFAULT 1,status TEXT NOT NULL DEFAULT 'PENDING',last_certificate_id INTEGER,last_fingerprint TEXT,last_deployed_at TEXT,last_error TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
		INSERT INTO dns_credentials(id,name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,created_at,updated_at) VALUES(1,'legacy','ALIYUN','example.com','key','secret',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO certificates(common_name,sans_json,issuer,serial_number,not_before,not_after,fingerprint,version,cert_path,chain_path,key_path,email,ca_directory,credential_id,created_at,updated_at) VALUES('example.com','["example.com","*.example.com"]','legacy','01','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','legacy-fingerprint',1,'cert','chain','key','ops@example.com','https://acme-staging-v02.api.letsencrypt.org/directory',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`
	if _, err := raw.Exec(legacySchema); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seriesValues, err := database.ListCertificateSeries(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(seriesValues) != 1 || seriesValues[0].CurrentCertificateID == nil || len(seriesValues[0].Domains) != 2 {
		t.Fatalf("旧证书系列迁移结果异常: %+v", seriesValues)
	}
	for table, column := range map[string]string{"certificates": "series_id", "certificate_orders": "series_id", "deployment_targets": "certificate_series_id"} {
		var count int
		if err := database.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("旧表 %s 未增加列 %s", table, column)
		}
	}
}

// TestDashboardOnEmptyDatabase 验证空数据库的运行概览统计保持为零。
func TestDashboardOnEmptyDatabase(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	value, err := database.Dashboard(context.Background(), "server", true, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if value.CertificateTotal != 0 || value.OrderRunning != 0 || value.AgentTotal != 0 {
		t.Fatalf("空数据库仪表盘应全部为零，实际为 %+v", value)
	}
}

// TestBackupCreatesConsistentSQLiteSnapshot 验证在线备份包含已经提交的数据且可独立打开。
func TestBackupCreatesConsistentSQLiteSnapshot(t *testing.T) {
	directory := t.TempDir()
	database, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateUser(context.Background(), "backup-admin", "hash", "备份管理员"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := database.Backup(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	copyDatabase, err := sql.Open("sqlite", "file:"+snapshot+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDatabase.Close()
	var count int
	if err := copyDatabase.QueryRow(`SELECT COUNT(*) FROM users WHERE username='backup-admin'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("一致快照缺少已提交用户: count=%d err=%v", count, err)
	}
}

// TestDeploymentTargetUsesExplicitExecutorType 验证部署目标必须明确选择本机或 Agent 执行器。
func TestDeploymentTargetUsesExplicitExecutorType(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	target, err := database.CreateTarget(ctx, DeploymentTarget{
		ExecutorType: ExecutorLocal,
		Name:         "本机 Nginx",
		CommonName:   "example.com",
		TargetAlias:  "nginx-public",
		Enabled:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := database.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExecutorType != ExecutorLocal || stored.AgentID != nil || stored.ExecutorName != "Server 本机执行器" {
		t.Fatalf("本机执行器契约异常: %+v", stored)
	}
	if _, err := database.CreateTarget(ctx, DeploymentTarget{
		ExecutorType: ExecutorAgent,
		Name:         "无身份 Agent",
		CommonName:   "example.com",
		TargetAlias:  "invalid-agent",
		Enabled:      true,
	}); err == nil {
		t.Fatal("AGENT 执行器缺少 agent_id 时应被数据库约束拒绝")
	}
}

// TestAgentCertificateRotationKeepsOneRecoverySerial 验证 Agent 证书轮换后仍保留上一序列号作为恢复窗口。
func TestAgentCertificateRotationKeepsOneRecoverySerial(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	expiresAt := time.Now().UTC().Add(180 * 24 * time.Hour)
	if err := database.CreateEnrollmentToken(ctx, "token", "edge", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Agent 创建回调构造首次注册的节点身份。
	agent, err := database.ConsumeEnrollmentToken(ctx, "token", time.Now(), func(name string) (Agent, error) {
		return Agent{ID: "agent-id", Name: name, CertificateSerial: "serial-1", CertificateExpiresAt: expiresAt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAgentCertificate(ctx, agent.ID, "serial-1", "serial-2", expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAgentCertificate(ctx, agent.ID, "serial-1", "serial-3", expiresAt); err != nil {
		t.Fatalf("使用仍在恢复窗口内的旧证书重复续签应成功: %v", err)
	}
	for _, serial := range []string{"serial-1", "serial-3"} {
		found, err := database.FindAgentBySerial(ctx, serial)
		if err != nil || found.ID != agent.ID {
			t.Fatalf("序列号 %s 应在恢复窗口内可用: agent=%+v err=%v", serial, found, err)
		}
	}
}

// TestInsertCertificateAssignsConcurrentVersions 验证同一域名的并发登记在事务内获得唯一递增版本。
func TestInsertCertificateAssignsConcurrentVersions(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "test", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	createTestSeries(t, database, credential.ID)

	versions := make(chan int, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		index := index
		wait.Add(1)
		// 插入协程模拟同一主域名证书的并发登记。
		go func() {
			defer wait.Done()
			certificate, insertErr := database.InsertCertificate(ctx, certificateFixture(credential.ID, index))
			if insertErr != nil {
				errors <- insertErr
				return
			}
			versions <- certificate.Version
		}()
	}
	wait.Wait()
	close(errors)
	for insertErr := range errors {
		t.Fatal(insertErr)
	}
	close(versions)
	actual := make([]int, 0, 2)
	for version := range versions {
		actual = append(actual, version)
	}
	sort.Ints(actual)
	if len(actual) != 2 || actual[0] != 1 || actual[1] != 2 {
		t.Fatalf("并发证书版本应为 [1 2]，实际为 %v", actual)
	}
}

// TestOpenRepairsLegacyDuplicateCertificateVersions 验证升级时会修复旧库重复版本并建立唯一约束。
func TestOpenRepairsLegacyDuplicateCertificateVersions(t *testing.T) {
	directory := t.TempDir()
	database, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "test", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		value := certificateFixture(credential.ID, index)
		now := nowText()
		if _, err := database.db.ExecContext(ctx, `INSERT INTO certificates(common_name,sans_json,issuer,serial_number,not_before,not_after,fingerprint,version,cert_path,chain_path,key_path,email,ca_directory,credential_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.CommonName, `["example.com"]`, value.Issuer, value.SerialNumber, timeText(value.NotBefore), timeText(value.NotAfter), value.Fingerprint, 1, value.CertPath, value.ChainPath, value.KeyPath, value.Email, value.CADirectory, value.CredentialID, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.db.QueryContext(ctx, `SELECT version FROM certificates WHERE common_name='example.com' ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("旧版本号修复异常: %v", versions)
	}
}

// TestReadyHonorsContextDeadline 验证数据库连接被占用时就绪探针会按期限失败而不是永久卡住。
func TestReadyHonorsContextDeadline(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	transaction, err := database.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := database.Ready(ctx); err == nil {
		t.Fatal("连接池被占用时 Ready 应在上下文到期后失败")
	}
}

// certificateFixture 构造用于存储层版本测试的完整证书元数据。
func certificateFixture(credentialID int64, index int) Certificate {
	now := time.Now().UTC()
	return Certificate{
		SeriesID: "series-test", CommonName: "example.com", SANs: []string{"example.com"}, Issuer: "test",
		SerialNumber: fmt.Sprintf("serial-%d", index), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		Fingerprint: fmt.Sprintf("%064x", index+1), CertPath: fmt.Sprintf("cert-%d", index), ChainPath: fmt.Sprintf("chain-%d", index),
		KeyPath: fmt.Sprintf("key-%d", index), Email: "ops@example.com", CADirectory: StagingDirectory, CredentialID: credentialID,
	}
}

// createTestSeries 创建证书版本和部署授权测试共用的固定系列。
func createTestSeries(t *testing.T, database *Store, credentialID int64) CertificateSeries {
	t.Helper()
	series, err := database.ResolveCertificateSeries(context.Background(), CertificateSeries{
		ID: "series-test", Code: "CERT-TEST000001", Name: "example.com", Domains: []string{"example.com"},
		DomainsHash: DomainSetHash([]string{"example.com"}), Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING",
		CADirectory: StagingDirectory, CredentialID: credentialID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return series
}

// TestCertificateSeriesIdentityIgnoresDomainOrder 验证相同 CA 下域名顺序变化不会创建第二个系列。
func TestCertificateSeriesIdentityIgnoresDomainOrder(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "series", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	firstDomains := []string{"example.com", "*.example.com", "api.example.com"}
	first, err := database.ResolveCertificateSeries(ctx, CertificateSeries{ID: "series-order-a", Code: "CERT-ORDERA", Name: "首个名称", Domains: firstDomains, DomainsHash: DomainSetHash(firstDomains), Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CADirectory: StagingDirectory, CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondDomains := []string{"api.example.com", "example.com", "*.example.com"}
	second, err := database.ResolveCertificateSeries(ctx, CertificateSeries{ID: "series-order-b", Code: "CERT-ORDERB", Name: "更新名称", Domains: secondDomains, DomainsHash: DomainSetHash(secondDomains), Email: "security@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CADirectory: StagingDirectory, CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Code != second.Code {
		t.Fatalf("同一域名集合必须复用稳定系列: first=%+v second=%+v", first, second)
	}
	if second.Name != first.Name || second.Email != first.Email || second.Revision != first.Revision {
		t.Fatalf("复用系列时不应隐式覆盖长期配置: first=%+v second=%+v", first, second)
	}
	second.Name, second.Email, second.AutoRenew = "显式更新名称", "security@example.com", false
	if err := database.UpdateCertificateSeries(ctx, second); err != nil {
		t.Fatal(err)
	}
	updated, err := database.FindCertificateSeries(ctx, second.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != second.Name || updated.Email != second.Email || updated.AutoRenew || updated.Revision != first.Revision+1 {
		t.Fatalf("显式更新系列配置未生效: updated=%+v", updated)
	}
}

// TestCompleteOrderDoesNotRegressSeriesCurrentVersion 验证乱序完成的旧任务不会覆盖系列较新版本。
func TestCompleteOrderDoesNotRegressSeriesCurrentVersion(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "current", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, database, credential.ID)
	seriesID := series.ID
	target, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorLocal, Name: "当前版本目标", CertificateSeriesID: &seriesID, CommonName: "example.com", TargetAlias: "current-version", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE deployment_targets SET status='SUCCESS' WHERE id=?`, target.ID); err != nil {
		t.Fatal(err)
	}
	firstOrder, err := database.CreateOrder(ctx, Order{SeriesID: series.ID, Domains: series.Domains, Email: "ops@example.com", CAEnvironment: series.CAEnvironment, CADirectory: series.CADirectory, CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	firstCertificate, err := database.InsertCertificate(ctx, certificateFixture(credential.ID, 20))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOrder(ctx, firstOrder.ID, firstCertificate.ID); err != nil {
		t.Fatal(err)
	}
	secondOrder, err := database.CreateOrder(ctx, Order{SeriesID: series.ID, Domains: series.Domains, Email: "ops@example.com", CAEnvironment: series.CAEnvironment, CADirectory: series.CADirectory, CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondCertificate, err := database.InsertCertificate(ctx, certificateFixture(credential.ID, 21))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOrder(ctx, secondOrder.ID, secondCertificate.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOrder(ctx, firstOrder.ID, firstCertificate.ID); err != nil {
		t.Fatal(err)
	}
	current, err := database.FindCurrentCertificate(ctx, series.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != secondCertificate.ID || current.Version != 2 {
		t.Fatalf("系列当前版本发生回退: current=%+v", current)
	}
	updatedTarget, err := database.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedTarget.Status != "PENDING" || updatedTarget.Revision <= target.Revision {
		t.Fatalf("新证书完成后关联目标应立即等待重新部署: target=%+v", updatedTarget)
	}
}

// TestAuditLogsSupportCursorFiltering 验证审计记录可以按动作筛选并使用递减主键游标翻页。
func TestAuditLogsSupportCursorFiltering(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "admin", "hash", "管理员")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := database.Audit(ctx, &user.ID, "UPDATE", "TEST_RESOURCE", fmt.Sprint(index), "测试"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := database.ListAuditLogs(ctx, 0, 2, "UPDATE", "TEST_RESOURCE")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].ID <= page.Items[1].ID {
		t.Fatalf("审计首分页结果异常: %+v", page)
	}
	next, err := database.ListAuditLogs(ctx, *page.NextCursor, 2, "UPDATE", "TEST_RESOURCE")
	if err != nil || len(next.Items) != 1 {
		t.Fatalf("审计游标下一页异常: page=%+v err=%v", next, err)
	}
}

// TestAmbiguousLegacyTargetRequiresBinding 验证同一首域名对应多个组合时旧目标不会被自动误绑。
func TestAmbiguousLegacyTargetRequiresBinding(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "ambiguous", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for index, domains := range [][]string{{"example.com", "a.example.com"}, {"example.com", "b.example.com"}} {
		_, err := database.ResolveCertificateSeries(ctx, CertificateSeries{ID: fmt.Sprintf("ambiguous-%d", index), Code: fmt.Sprintf("CERT-AMBIGUOUS%d", index), Name: fmt.Sprintf("组合 %d", index), Domains: domains, DomainsHash: DomainSetHash(domains), Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CADirectory: StagingDirectory, CredentialID: credential.ID})
		if err != nil {
			t.Fatal(err)
		}
	}
	target, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorLocal, Name: "旧 Nginx", CommonName: "example.com", TargetAlias: "legacy-nginx", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.migrateCertificateSeries(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := database.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CertificateSeriesID != nil || stored.Status != "NEEDS_BINDING" {
		t.Fatalf("歧义旧目标必须等待人工绑定: %+v", stored)
	}
}

// TestDesiredTargetsDoesNotBlockSingleSQLiteConnection 验证 Agent 对账不会因嵌套查询占满唯一 SQLite 连接。
func TestDesiredTargetsDoesNotBlockSingleSQLiteConnection(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	expiresAt := time.Now().UTC().Add(180 * 24 * time.Hour)
	if err := database.CreateEnrollmentToken(ctx, "desired-target-token", "edge", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Agent 创建回调构造用于期望状态查询的节点身份。
	agent, err := database.ConsumeEnrollmentToken(ctx, "desired-target-token", time.Now(), func(name string) (Agent, error) {
		return Agent{ID: "desired-target-agent", Name: name, CertificateSerial: "desired-target-serial", CertificateExpiresAt: expiresAt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "desired", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, database, credential.ID)
	if _, err := database.CreateTarget(ctx, DeploymentTarget{
		ExecutorType:        ExecutorAgent,
		AgentID:             &agent.ID,
		Name:                "Agent Nginx",
		CertificateSeriesID: &series.ID,
		CommonName:          "example.com",
		TargetAlias:         "nginx-agent",
		Enabled:             true,
	}); err != nil {
		t.Fatal(err)
	}

	values, err := database.DesiredTargets(ctx, agent.ID, "https://127.0.0.1:9443", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("查询 Agent 期望状态不应阻塞唯一 SQLite 连接: %v", err)
	}
	if len(values) != 1 || values[0].Certificate != nil {
		t.Fatalf("无可用证书时应返回一个不带证书的目标，实际为 %+v", values)
	}
	if _, err := database.Dashboard(ctx, "server", true, 30*24*time.Hour); err != nil {
		t.Fatalf("Agent 对账后管理端查询应能继续使用数据库连接: %v", err)
	}
}

// TestAgentDeploymentResultRequiresCurrentAssignmentAndDeduplicates 验证 Agent 只能上报当前授权证书且重复结果不会刷爆历史表。
func TestAgentDeploymentResultRequiresCurrentAssignmentAndDeduplicates(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	expiresAt := time.Now().UTC().Add(180 * 24 * time.Hour)
	if err := database.CreateEnrollmentToken(ctx, "report-token", "edge", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Agent 创建回调构造用于部署上报授权测试的节点身份。
	agent, err := database.ConsumeEnrollmentToken(ctx, "report-token", time.Now(), func(name string) (Agent, error) {
		return Agent{ID: "report-agent", Name: name, CertificateSerial: "report-serial", CertificateExpiresAt: expiresAt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := database.CreateDNSCredential(ctx, DNSCredential{Name: "test", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "key", AccessKeySecret: "secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series := createTestSeries(t, database, credential.ID)
	target, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorAgent, AgentID: &agent.ID, Name: "Nginx", CertificateSeriesID: &series.ID, CommonName: "example.com", TargetAlias: "nginx", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := database.InsertCertificate(ctx, certificateFixture(credential.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE certificate_series SET current_certificate_id=? WHERE id=?`, certificate.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordDeployment(ctx, agent.ID, target.ID, target.Revision, certificate.ID, "forged", "SUCCESS", "证书已原子部署", nil); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("事务内必须拒绝伪造指纹: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := database.RecordDeployment(ctx, agent.ID, target.ID, target.Revision, certificate.ID, certificate.Fingerprint, "SUCCESS", "证书已原子部署", nil); err != nil {
			t.Fatal(err)
		}
	}
	records, err := database.ListDeploymentRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("十分钟内完全相同的结果应合并为一条，实际 %d 条", len(records))
	}
}
