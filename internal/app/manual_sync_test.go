// 本文件验证本机自动对账只修复变更，并保留显式同步和文件自愈。
//
// @author Andersen
package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/twodogleee/acme4go/internal/deploy"
)

// TestLocalReconcileSkipsHealthyFiles 验证重复检查不重写文件、不刷新切换时间或刷部署记录。
func TestLocalReconcileSkipsHealthyFiles(t *testing.T) {
	s, target, bundle := followupDeploymentFixture(t)
	ctx := context.Background()
	if err := s.ReconcileLocalTargets(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Config.DeploymentDir, target.TargetAlias, "current", "private-key.pem")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Store.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.ReconcileLocalTargets(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Store.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	records, err := s.Store.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 1 || !os.SameFile(before, after) || !first.LastDeployedAt.Equal(*current.LastDeployedAt) {
		t.Fatalf("健康检查产生部署副作用：records=%d err=%v", len(records), err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileLocalTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if err := deploy.ValidateMaterialized(s.Config.DeploymentDir, target.TargetAlias, bundle.SANs, bundle.Fingerprint); err != nil {
		t.Fatal(err)
	}
	records, err = s.Store.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("文件修复没有独立记录 %d %v", len(records), err)
	}
	if err := s.SyncTarget(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	records, err = s.Store.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 3 {
		t.Fatalf("显式同步被跳过 %d %v", len(records), err)
	}
	if err := s.Store.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileLocalTargets(ctx); err != nil {
		t.Fatal(err)
	}
	records, err = s.Store.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 4 {
		t.Fatalf("配置变更被错误跳过 %d %v", len(records), err)
	}
}
