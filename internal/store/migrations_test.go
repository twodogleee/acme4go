// 本文件验证 Go 数据库迁移的版本记录、单次执行和失败恢复边界。
//
// @author Andersen
package store

import (
	"context"
	"errors"
	"testing"
)

// TestOpenRecordsBuiltInSchemaMigrations 验证新库初始化后记录全部内置迁移版本。
func TestOpenRecordsBuiltInSchemaMigrations(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	rows, err := database.db.QueryContext(context.Background(), `SELECT version,name,applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	expectedNames := []string{"certificate_series", "certificate_series_version_constraint", "operational_indexes", "order_and_alert_reliability", "dns_providers", "portable_material_paths", "cloud_certificate_deployment", "agent_manual_sync", "unified_credentials", "cloud_credential_schema", "credential_multiple_zones"}
	index := 0
	for rows.Next() {
		var version int
		var name, appliedAt string
		if err := rows.Scan(&version, &name, &appliedAt); err != nil {
			t.Fatal(err)
		}
		if index >= len(expectedNames) || version != index+1 || name != expectedNames[index] || appliedAt == "" {
			t.Fatalf("数据库迁移记录异常: version=%d name=%q applied_at=%q", version, name, appliedAt)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(expectedNames) {
		t.Fatalf("内置迁移记录数量应为 %d，实际为 %d", len(expectedNames), index)
	}
}

// TestSchemaMigrationRunsOnlyOnce 验证相同版本重复检查时不会再次调用迁移函数。
func TestSchemaMigrationRunsOnlyOnce(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	callCount := 0
	migration := schemaMigration{
		Version: 1000,
		Name:    "test_once",
		// 成功迁移回调累计实际执行次数。
		Run: func(context.Context) error {
			callCount++
			return nil
		},
	}
	ctx := context.Background()
	if err := database.runSchemaMigration(ctx, migration); err != nil {
		t.Fatal(err)
	}
	if err := database.runSchemaMigration(ctx, migration); err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("同一迁移版本应只执行一次，实际执行 %d 次", callCount)
	}
}

// TestSchemaMigrationRejectsVersionReuse 验证已登记版本不能被不同名称的迁移复用。
func TestSchemaMigrationRejectsVersionReuse(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	first := schemaMigration{
		Version: 1002,
		Name:    "test_original",
		// 首次迁移回调模拟成功完成的稳定迁移。
		Run: func(context.Context) error { return nil },
	}
	if err := database.runSchemaMigration(ctx, first); err != nil {
		t.Fatal(err)
	}
	reused := schemaMigration{
		Version: first.Version,
		Name:    "test_reused",
		// 复用版本回调不应获得执行机会。
		Run: func(context.Context) error { return nil },
	}
	if err := database.runSchemaMigration(ctx, reused); err == nil {
		t.Fatal("不同迁移名称复用已登记版本时应被拒绝")
	}
}

// TestRunMigrationsRejectsNewerDatabase 验证旧二进制不能打开更高迁移版本的数据库。
func TestRunMigrationsRejectsNewerDatabase(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	futureVersion := latestSchemaMigrationVersion + 1
	if _, err := database.db.ExecContext(context.Background(), `INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)`, futureVersion, "future_migration", nowText()); err != nil {
		t.Fatal(err)
	}
	if err := database.runMigrations(context.Background()); err == nil {
		t.Fatal("数据库版本高于当前程序时应拒绝启动")
	}
}

// TestReopenCurrentDatabaseDoesNotRewriteSchema 验证最新数据库重复启动不会删除或重建索引。
func TestReopenCurrentDatabaseDoesNotRewriteSchema(t *testing.T) {
	directory := t.TempDir()
	database, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := database.db.QueryRowContext(context.Background(), `PRAGMA schema_version`).Scan(&before); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var after int
	if err := database.db.QueryRowContext(context.Background(), `PRAGMA schema_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("最新数据库重复启动不应改写 Schema，启动前版本=%d，启动后版本=%d", before, after)
	}
}

// TestFailedSchemaMigrationIsNotRecorded 验证失败迁移不会被错误标记为已完成。
func TestFailedSchemaMigrationIsNotRecorded(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	expectedErr := errors.New("预期迁移失败")
	migration := schemaMigration{
		Version: 1001,
		Name:    "test_failure",
		// 失败迁移回调返回可识别的预期错误。
		Run: func(context.Context) error {
			return expectedErr
		},
	}
	if err := database.runSchemaMigration(context.Background(), migration); !errors.Is(err, expectedErr) {
		t.Fatalf("迁移应返回原始失败原因，实际为 %v", err)
	}
	var count int
	if err := database.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, migration.Version).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("失败迁移不得写入完成记录")
	}
}
