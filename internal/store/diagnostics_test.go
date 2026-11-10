// 本文件验证诊断入口不创建数据库、不运行迁移且拒绝写入。
//
// @author Andersen
package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDiagnosticStoreIsReadOnly 验证旧版本数据库只读打开后版本不变，普通写入被拒绝。
func TestDiagnosticStoreIsReadOnly(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	database, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := database.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != latestSchemaMigrationVersion {
		t.Fatalf("相对路径契约未登记：%d %v", version, err)
	}
	if _, err := database.db.Exec(`DELETE FROM schema_migrations WHERE version>=?`, portableMaterialPathsMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = OpenReadOnly(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != dnsProvidersMigrationVersion {
		t.Fatalf("诊断执行了迁移：%d %v", version, err)
	}
	if _, err := database.db.Exec(`INSERT INTO users(username,password_hash,display_name,created_at) VALUES('unwanted','hash','name','now')`); err == nil {
		t.Fatal("诊断数据库允许写入")
	}
}

// TestDiagnosticStoreDoesNotInitializeMissingDirectory 验证错误恢复目录不会被初始化成空数据库。
func TestDiagnosticStoreDoesNotInitializeMissingDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	if database, err := OpenReadOnly(context.Background(), directory); err == nil {
		database.Close()
		t.Fatal("不存在的数据库被诊断入口创建")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("诊断创建了目录：%v", err)
	}
}
