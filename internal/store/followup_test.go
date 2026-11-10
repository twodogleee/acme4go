// 本文件验证密码版本约束、本机部署执行快照及配置切换协调。
//
// @author Andersen
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPasswordChangesInvalidatePendingAuthentication 验证改密前已验证的凭据不能在改密后创建会话或覆盖新密码。
func TestPasswordChangesInvalidatePendingAuthentication(t *testing.T) {
	database, _, _ := reliabilityStore(t)
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "auth-fixture", "old-verified-hash", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if err := database.CreateSession(ctx, "before", "csrf", user.ID, user.Password, expires); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdatePassword(ctx, user.ID, user.Password, "new-verified-hash"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SessionUser(ctx, "before", time.Now()); err == nil {
		t.Fatal("改密没有撤销已有会话")
	}
	if err := database.CreateSession(ctx, "stale", "csrf", user.ID, user.Password, expires); !errors.Is(err, ErrCredentialsChanged) {
		t.Fatalf("旧凭据创建会话：%v", err)
	}
	if err := database.UpdatePassword(ctx, user.ID, user.Password, "stale-overwrite"); !errors.Is(err, ErrCredentialsChanged) {
		t.Fatalf("旧凭据覆盖新密码：%v", err)
	}
	if err := database.CreateSession(ctx, "current", "csrf", user.ID, "new-verified-hash", expires); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SessionUser(ctx, "current", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SessionUser(ctx, "stale", time.Now()); err == nil {
		t.Fatal("旧凭据遗留了有效会话")
	}
}

// TestLocalResultsRequireCurrentSnapshot 验证旧修订、旧证书和停用系列均不能覆盖当前部署状态。
func TestLocalResultsRequireCurrentSnapshot(t *testing.T) {
	database, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	certificate, err := database.InsertCertificate(ctx, certificateFixture(credentialID, 201))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE certificate_series SET current_certificate_id=? WHERE id=?`, certificate.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorLocal, CertificateSeriesID: &series.ID, Name: "local", TargetAlias: "old", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, target.Revision, certificate.ID, certificate.Fingerprint, "SUCCESS", "已部署"); err != nil {
		t.Fatal(err)
	}
	oldRevision := target.Revision
	target.TargetAlias = "new"
	if err := database.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, oldRevision, certificate.ID, certificate.Fingerprint, "SUCCESS", "旧任务"); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("接受旧任务：%v", err)
	}
	target, err = database.FindTarget(ctx, target.ID)
	if err != nil || target.Status != "PENDING" {
		t.Fatalf("旧回执覆盖新配置：%s %v", target.Status, err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, target.Revision, certificate.ID, "forged", "SUCCESS", "伪造指纹"); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("接受错误指纹：%v", err)
	}
	newCertificate, err := database.InsertCertificate(ctx, certificateFixture(credentialID, 202))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE certificate_series SET current_certificate_id=? WHERE id=?`, newCertificate.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, target.Revision, certificate.ID, certificate.Fingerprint, "FAILED", "旧证书"); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("接受旧证书：%v", err)
	}
	series.Enabled = false
	if err := database.UpdateCertificateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordLocalDeployment(ctx, target.ID, target.Revision, newCertificate.ID, newCertificate.Fingerprint, "SUCCESS", "停用系列"); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("接受停用系列：%v", err)
	}
	records, err := database.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("拒绝的回执污染历史：%d %v", len(records), err)
	}
}

// TestLocalDeploymentCoordinatesChanges 验证文件操作期间配置变更可取消等待，普通查询不被阻塞。
func TestLocalDeploymentCoordinatesChanges(t *testing.T) {
	database, series, _ := reliabilityStore(t)
	ctx := context.Background()
	target, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorLocal, CertificateSeriesID: &series.ID, Name: "local", TargetAlias: "guard", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	// 持有真实协调权模拟正在执行的本机文件切换，退出时等待协程结束。
	go func() {
		// 受保护回调模拟文件切换尚未完成。
		done <- database.WithLocalDeployment(ctx, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	released := false
	// 清理回调避免测试失败时遗留持锁协程。
	defer func() {
		if !released {
			close(release)
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	if _, err := database.FindTarget(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	// 所有会改变部署期望或删除目标的入口都必须参与协调。
	operations := map[string]func(context.Context) error{
		// 目标编辑必须等待正在进行的文件切换。
		"update-target": func(ctx context.Context) error { return database.UpdateTarget(ctx, target) },
		// 删除不能让旧任务在删除完成后继续写入。
		"delete-target": func(ctx context.Context) error { return database.DeleteTarget(ctx, target.ID) },
		// 系列停用或配置变更与物化串行提交。
		"update-series": func(ctx context.Context) error { return database.UpdateCertificateSeries(ctx, series) },
		// 新证书成为当前版本前必须等待旧版本物化完成。
		"complete-order": func(ctx context.Context) error { return database.CompleteOrder(ctx, 0, 0) },
		// 第二个部署不能并发切换相同文件指针。
		"second-deployment": func(ctx context.Context) error {
			// 等待被取消时该回调不应进入。
			return database.WithLocalDeployment(ctx, func() error { t.Error("取消后仍开始部署"); return nil })
		},
	}
	for name, operation := range operations {
		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		err := operation(waitCtx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s 没有等待本机文件切换：%v", name, err)
		}
	}
	close(release)
	err = <-done
	released = true
	if err != nil {
		t.Fatal(err)
	}
	target.TargetAlias = "after-deployment"
	if err := database.UpdateTarget(ctx, target); err != nil {
		t.Fatalf("部署结束后未释放配置变更：%v", err)
	}
	updated, err := database.FindTarget(ctx, target.ID)
	if err != nil || updated.Revision != target.Revision+1 || updated.Status != "PENDING" {
		t.Fatalf("部署后的配置状态错误：%+v %v", updated, err)
	}
}
