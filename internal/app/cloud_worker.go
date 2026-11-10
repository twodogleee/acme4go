// 本文件执行可恢复云任务：上传与部署分阶段登记，云工作者独立于证书签发和文件部署。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/store"
)

// wakeCloud 合并唤醒请求，避免签发完成被外部 API 阻塞。
func (s *Services) wakeCloud() {
	select {
	case s.cloudWake <- struct{}{}:
	default:
	}
}

// runCloudWorker 单工作者串行处理云端更新，重启后安全接管未完成任务。
func (s *Services) runCloudWorker(ctx context.Context) {
	if err := s.Store.RecoverCloudJobs(ctx); err != nil {
		s.logger.Error("恢复云任务失败", "error", err)
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.scheduleCloudPlans(ctx)
		for ctx.Err() == nil {
			job, err := s.Store.NextCloudJob(ctx)
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			if err != nil {
				s.logger.Error("读取云任务失败", "error", err)
				break
			}
			if !s.executeCloudJob(ctx, job) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.cloudWake:
		}
	}
}

// scheduleCloudPlans 扫描跟随本地系列的上传/部署配置，固定当前版本并限制失败重试。
func (s *Services) scheduleCloudPlans(ctx context.Context) {
	plans, err := s.Store.ListCloudPlans(ctx)
	if err != nil {
		s.logger.Error("扫描云配置失败", "error", err)
		return
	}
	for _, p := range plans {
		if ctx.Err() != nil {
			return
		}
		if !p.AutoSync || !p.Enabled || p.SeriesID == nil {
			continue
		}
		account, err := s.Store.FindCredential(ctx, p.CredentialID)
		if err != nil || !account.Enabled || !account.CloudEnabled || account.Provider != "ALIYUN" {
			continue
		}
		series, err := s.Store.FindCertificateSeries(ctx, *p.SeriesID, s.Config.RenewBefore)
		if err != nil || !series.Enabled || series.CADirectory == store.StagingDirectory {
			continue
		}
		cert, err := s.Store.FindCurrentCertificate(ctx, *p.SeriesID, s.Config.RenewBefore)
		if err != nil {
			continue
		}
		needed, err := s.Store.CloudAutoNeeded(ctx, p, cert.ID)
		if err != nil || !needed {
			continue
		}
		if _, err := s.Store.EnqueueCloudJob(ctx, p, &cert.ID); err != nil && !errors.Is(err, store.ErrCloudBusy) {
			s.logger.Error("云任务入队失败", "plan_id", p.ID, "error", err)
		}
	}
}

// executeCloudJob 保存阶段结果，关闭时留待重启接管；false 表示本轮应停止，避免存储失败时忙循环。
func (s *Services) executeCloudJob(parent context.Context, job store.CloudJob) bool {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	job.Status = "RUNNING"
	if err := s.Store.SetCloudJob(ctx, job); err != nil {
		s.logger.Error("开始云任务失败", "job_id", job.ID, "error", err)
		return false
	}
	err := s.performCloudJob(ctx, &job)
	if parent.Err() != nil {
		return false
	}
	if err != nil {
		job.Status = "FAILED"
		job.Message = err.Error()
	} else {
		job.Status = "SUCCESS"
		job.Stage = "COMPLETE"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if saveErr := s.Store.SetCloudJob(finishCtx, job); saveErr != nil {
		s.logger.Error("保存云任务结果失败", "job_id", job.ID, "error", saveErr)
		return false
	}
	if err != nil {
		s.PublishAlert(Alert{Event: "certificate.cloud.failed", Severity: "ERROR", ResourceType: "CLOUD_PLAN", ResourceID: formatID(job.PlanID), Message: "云任务失败：" + job.Message})
	}
	return true
}

// cloudStage 在副作用之前保存执行进度。
func (s *Services) cloudStage(ctx context.Context, job *store.CloudJob, stage string) error {
	job.Stage = stage
	return s.Store.SetCloudJob(ctx, *job)
}

// performCloudJob 对来源、版本和域名重新校验，先上传再按需更新产品。
func (s *Services) performCloudJob(ctx context.Context, job *store.CloudJob) error {
	p, err := s.Store.FindCloudPlan(ctx, job.PlanID)
	if err != nil || !p.Enabled || p.Revision != job.Revision {
		return errors.New("云配置已停用或变更")
	}
	credential, err := s.cloudCredential(ctx, p.CredentialID)
	if err != nil {
		return err
	}
	expectedFingerprint := ""
	if p.SeriesID != nil {
		series, err := s.Store.FindCertificateSeries(ctx, *p.SeriesID, s.Config.RenewBefore)
		if err != nil || !series.Enabled || series.CADirectory == store.StagingDirectory {
			return errors.New("证书系列已停用或使用测试 CA")
		}
		current, err := s.Store.FindCurrentCertificate(ctx, *p.SeriesID, s.Config.RenewBefore)
		if err != nil || job.CertificateID == nil || current.ID != *job.CertificateID {
			return errors.New("排队证书已不是当前版本，请重新执行最新版本")
		}
		expectedFingerprint = current.Fingerprint
		if err := s.cloudStage(ctx, job, "UPLOAD"); err != nil {
			return err
		}
		remoteID, err := s.Store.FindCloudUpload(ctx, p.CredentialID, current.Fingerprint)
		if errors.Is(err, store.ErrNotFound) {
			_, bundle, err := s.CertificateBundle(ctx, current.ID)
			if err != nil {
				return errors.New("读取或验证本地证书材料失败")
			}
			if p.Kind == "DEPLOY" {
				adapter, ok := s.Cloud.Deployers[p.Product]
				if !ok {
					return errors.New("云产品适配器不存在")
				}
				domain, _, err := adapter.Validate(p.Config)
				if err != nil {
					return err
				}
				local := cloud.Certificate{Domains: series.Domains, NotAfter: current.NotAfter.Format(time.RFC3339)}
				if err := local.Covers(domain); err != nil {
					return err
				}
			}
			uploaded, err := s.Cloud.Catalog.Upload(ctx, credential, cloud.Material{Name: "acme4go-" + cryptoutil.TokenHash(current.Fingerprint), Chain: bundle.FullChain, Key: bundle.PrivateKey, Fingerprint: current.Fingerprint})
			if err != nil {
				return err
			}
			remoteID = uploaded.ID
			if err := s.Store.SaveCloudUpload(ctx, p.CredentialID, current.Fingerprint, remoteID); err != nil {
				return errors.New("证书已上传，但本地映射登记失败，可重试恢复")
			}
		} else if err != nil {
			return errors.New("读取云证书映射失败")
		}
		job.RemoteCertificateID = remoteID
		if err := s.Store.SetCloudJob(ctx, *job); err != nil {
			return errors.New("保存云证书上传结果失败")
		}
	}
	if err := s.cloudStage(ctx, job, "CHECK_CERTIFICATE"); err != nil {
		return err
	}
	cert, err := s.Cloud.Catalog.Get(ctx, credential, job.RemoteCertificateID)
	if err != nil {
		return err
	}
	if expectedFingerprint != "" {
		if cert.Fingerprint == "" {
			return errors.New("云证书已存在，但未返回 SHA-256 指纹，无法完成证书校验")
		}
		if !strings.EqualFold(expectedFingerprint, cert.Fingerprint) {
			return fmt.Errorf("云证书 #%s 已存在，但 SHA-256 指纹与本地版本不一致，已停止后续操作（本地：%s；云端：%s）", job.RemoteCertificateID, expectedFingerprint, cert.Fingerprint)
		}
	}
	if p.Kind == "UPLOAD" {
		job.Message = "证书已上传到阿里云证书中心"
		return nil
	}
	adapter, ok := s.Cloud.Deployers[p.Product]
	if !ok {
		return errors.New("云产品适配器不存在")
	}
	domain, _, err := adapter.Validate(p.Config)
	if err != nil {
		return err
	}
	if err := cert.Covers(domain); err != nil {
		return err
	}
	// 已经成功的外部操作在进程崩溃后可通过云端读取直接确认。
	verify := func(checkCtx context.Context) (cloud.Verification, error) {
		return cloud.VerifyDeployment(checkCtx, adapter, credential, p.Config, cert)
	}
	verification, err := verify(ctx)
	if err != nil {
		return err
	}
	if !verification.Verified {
		if err := s.cloudStage(ctx, job, "DEPLOY"); err != nil {
			return err
		}
		if err := adapter.Deploy(ctx, credential, p.Config, cert); err != nil {
			return err
		}
		if err := s.cloudStage(ctx, job, "VERIFY"); err != nil {
			return err
		}
		if err := s.waitCloudVerification(ctx, job, verify, 3*time.Second); err != nil {
			return err
		}
	}
	job.Message = "云产品已确认使用新证书"
	return nil
}

// waitCloudVerification 提交后立即核验，后续限频轮询并仅在诊断变化时保存进度。
func (s *Services) waitCloudVerification(ctx context.Context, job *store.CloudJob, verify func(context.Context) (cloud.Verification, error), interval time.Duration) error {
	lastMessage := "尚未取得部署后的核验结果"
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return cloudVerificationInterrupted(ctx.Err(), lastMessage)
		}
		result, err := verify(ctx)
		if ctx.Err() != nil {
			return cloudVerificationInterrupted(ctx.Err(), lastMessage)
		}
		if err != nil {
			return fmt.Errorf("云端已接受部署，但查询生效结果失败：%w；上次核验：%s", err, lastMessage)
		}
		if result.Verified {
			return nil
		}
		if result.Message != "" {
			lastMessage = result.Message
		}
		message := "云端已接受部署，等待确认：" + lastMessage
		if job.Message != message {
			job.Message = message
			if err := s.Store.SetCloudJob(ctx, *job); err != nil {
				if ctx.Err() != nil {
					return cloudVerificationInterrupted(ctx.Err(), lastMessage)
				}
				return fmt.Errorf("保存云部署核验进度失败：%w", err)
			}
		}
		select {
		case <-ctx.Done():
			return cloudVerificationInterrupted(ctx.Err(), lastMessage)
		case <-ticker.C:
		}
	}
}

// cloudVerificationInterrupted 区分本地确认超时与部署拒绝，保留最近一次有效的核验原因。
func cloudVerificationInterrupted(err error, detail string) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("云端已接受部署，但本地确认超时，不代表云端部署失败；最近核验：%s。可检查云端状态后重新执行，系统会先核验已有部署", detail)
}
