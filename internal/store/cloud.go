// 本文件持久化基于共享凭据的独立上传/部署配置、证书映射和可恢复任务。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"time"
)

// cloudSchema 创建独立云部署数据域。
//
//go:embed cloud_schema.sql
var cloudSchema string

// ErrCloudBusy 表示配置正在使用或唯一目标已存在。
var ErrCloudBusy = errors.New("云配置正在执行、被引用或已经存在")

// CloudPlan 是上传规则或产品部署目标，二者可以独立存在。
type CloudPlan struct {
	ID                  int64           `json:"id"`                  // ID 是配置主键。
	CredentialID        int64           `json:"credentialId"`        // CredentialID 是共享凭据主键。
	SeriesID            *string         `json:"seriesId"`            // SeriesID 非空表示跟随本地证书系列。
	Name                string          `json:"name"`                // Name 是任务配置名称。
	Kind                string          `json:"kind"`                // Kind 是 UPLOAD 或 DEPLOY。
	Product             string          `json:"product"`             // Product 是部署适配器 ID；上传规则为空。
	RemoteCertificateID string          `json:"remoteCertificateId"` // RemoteCertificateID 非空表示固定使用已有云证书。
	Config              json.RawMessage `json:"config"`              // Config 是适配器规范化后的非秘密配置。
	AutoSync            bool            `json:"autoSync"`            // AutoSync 表示自动上传或部署新的本地版本。
	Enabled             bool            `json:"enabled"`             // Enabled 控制手动和自动执行。
	Revision            int64           `json:"revision"`            // Revision 防止旧任务使用新配置。
}

// CloudJob 记录执行阶段和选定版本，不存储云凭证或证书材料。
type CloudJob struct {
	ID                  int64  `json:"id"`                  // ID 是任务主键。
	PlanID              int64  `json:"planId"`              // PlanID 是关联规则。
	PlanName            string `json:"planName"`            // PlanName 是展示用规则名称。
	Revision            int64  `json:"revision"`            // Revision 是入队时配置版本。
	CertificateID       *int64 `json:"certificateId"`       // CertificateID 是入队时选定的本地证书版本。
	RemoteCertificateID string `json:"remoteCertificateId"` // RemoteCertificateID 是实际上传或部署的云证书。
	Status              string `json:"status"`              // Status 是排队、执行、成功或失败状态。
	Stage               string `json:"stage"`               // Stage 保存失败位置及恢复所需执行阶段。
	Message             string `json:"message"`             // Message 是不含敏感材料的执行结果。
	CreatedAt           string `json:"createdAt"`           // CreatedAt 是 UTC 创建时间。
	UpdatedAt           string `json:"updatedAt"`           // UpdatedAt 是最近执行更新时间。
}

// migrateCloud 幂等安装云领域数据表。
func (s *Store) migrateCloud(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, cloudSchema)
	return err
}

// cloudPlanColumns 保证单项和列表扫描顺序一致。
const cloudPlanColumns = `id,credential_id,series_id,name,kind,product,remote_certificate_id,config_json,auto_sync,enabled,revision`

// scanCloudPlan 解码非秘密 JSON 配置。
func scanCloudPlan(row interface {
	// Scan 从 SQL 行读取固定列。
	Scan(...any) error
}) (CloudPlan, error) {
	var v CloudPlan
	var series sql.NullString
	var config string
	err := row.Scan(&v.ID, &v.CredentialID, &series, &v.Name, &v.Kind, &v.Product, &v.RemoteCertificateID, &config, &v.AutoSync, &v.Enabled, &v.Revision)
	v.SeriesID = nullableString(series)
	v.Config = json.RawMessage(config)
	if isNotFound(err) {
		err = ErrNotFound
	}
	return v, err
}

// ListCloudPlans 列出仍存在的规则与目标。
func (s *Store) ListCloudPlans(ctx context.Context) ([]CloudPlan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cloudPlanColumns+` FROM cloud_plans WHERE deleted=0 ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []CloudPlan{}
	for rows.Next() {
		v, err := scanCloudPlan(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// FindCloudPlan 查询未删除的规则。
func (s *Store) FindCloudPlan(ctx context.Context, id int64) (CloudPlan, error) {
	return scanCloudPlan(s.db.QueryRowContext(ctx, `SELECT `+cloudPlanColumns+` FROM cloud_plans WHERE id=? AND deleted=0`, id))
}

// SaveCloudPlan 原子阻止运行中的配置修改，并递增修订号。
func (s *Store) SaveCloudPlan(ctx context.Context, v CloudPlan) (int64, error) {
	if v.ID == 0 {
		result, err := s.db.ExecContext(ctx, `INSERT INTO cloud_plans(credential_id,series_id,name,kind,product,remote_certificate_id,config_json,auto_sync,enabled) VALUES(?,?,?,?,?,?,?,?,?)`, v.CredentialID, nullable(v.SeriesID), v.Name, v.Kind, v.Product, v.RemoteCertificateID, string(v.Config), v.AutoSync, v.Enabled)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cloud_plans SET credential_id=?,series_id=?,name=?,kind=?,product=?,remote_certificate_id=?,config_json=?,auto_sync=?,enabled=?,revision=revision+1 WHERE id=? AND deleted=0 AND NOT EXISTS(SELECT 1 FROM cloud_jobs WHERE plan_id=? AND status IN ('QUEUED','RUNNING'))`, v.CredentialID, nullable(v.SeriesID), v.Name, v.Kind, v.Product, v.RemoteCertificateID, string(v.Config), v.AutoSync, v.Enabled, v.ID, v.ID)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return 0, ErrCloudBusy
	}
	return v.ID, nil
}

// DeleteCloudPlan 软删除配置，保留任务历史及云端资源。
func (s *Store) DeleteCloudPlan(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE cloud_plans SET deleted=1,enabled=0 WHERE id=? AND deleted=0 AND NOT EXISTS(SELECT 1 FROM cloud_jobs WHERE plan_id=? AND status IN ('QUEUED','RUNNING'))`, id, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrCloudBusy
	}
	return nil
}

// FindCloudUpload 查询同一凭据下已经上传的证书指纹映射。
func (s *Store) FindCloudUpload(ctx context.Context, credentialID int64, fingerprint string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT remote_certificate_id FROM cloud_uploads WHERE credential_id=? AND fingerprint=?`, credentialID, fingerprint).Scan(&id)
	if isNotFound(err) {
		err = ErrNotFound
	}
	return id, err
}

// SaveCloudUpload 在上传完成后立即登记映射，后续部署失败不会重复上传。
func (s *Store) SaveCloudUpload(ctx context.Context, credentialID int64, fingerprint, remoteID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cloud_uploads(credential_id,fingerprint,remote_certificate_id) VALUES(?,?,?) ON CONFLICT(credential_id,fingerprint) DO UPDATE SET remote_certificate_id=excluded.remote_certificate_id`, credentialID, fingerprint, remoteID)
	return err
}

// EnqueueCloudJob 使用条件插入阻止禁用、旧修订及同目标重复任务。
func (s *Store) EnqueueCloudJob(ctx context.Context, p CloudPlan, certificateID *int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO cloud_jobs(plan_id,revision,certificate_id,remote_certificate_id,status,created_at,updated_at) SELECT id,revision,?,remote_certificate_id,'QUEUED',?,? FROM cloud_plans WHERE id=? AND revision=? AND enabled=1 AND deleted=0 AND EXISTS(SELECT 1 FROM dns_credentials c WHERE c.id=cloud_plans.credential_id AND c.enabled=1 AND c.cloud_enabled=1 AND c.provider='ALIYUN') AND NOT EXISTS(SELECT 1 FROM cloud_jobs WHERE plan_id=? AND status IN ('QUEUED','RUNNING'))`, nullableID(certificateID), nowText(), nowText(), p.ID, p.Revision, p.ID)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return 0, ErrCloudBusy
	}
	return result.LastInsertId()
}

// cloudJobColumns 是带规则名称的任务扫描契约。
const cloudJobColumns = `j.id,j.plan_id,p.name,j.revision,j.certificate_id,j.remote_certificate_id,j.status,j.stage,j.message,j.created_at,j.updated_at`

// scanCloudJob 读取任务及可空本地证书主键。
func scanCloudJob(row interface {
	// Scan 从 SQL 行读取固定列。
	Scan(...any) error
}) (CloudJob, error) {
	var v CloudJob
	var cert sql.NullInt64
	err := row.Scan(&v.ID, &v.PlanID, &v.PlanName, &v.Revision, &cert, &v.RemoteCertificateID, &v.Status, &v.Stage, &v.Message, &v.CreatedAt, &v.UpdatedAt)
	v.CertificateID = nullableInt64(cert)
	if isNotFound(err) {
		err = ErrNotFound
	}
	return v, err
}

// ListCloudJobs 用游标分页读取任务历史。
func (s *Store) ListCloudJobs(ctx context.Context, before int64) ([]CloudJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cloudJobColumns+` FROM cloud_jobs j JOIN cloud_plans p ON p.id=j.plan_id WHERE (?=0 OR j.id<?) ORDER BY j.id DESC LIMIT 50`, before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []CloudJob{}
	for rows.Next() {
		v, err := scanCloudJob(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// FindCloudJob 查询单个持久任务。
func (s *Store) FindCloudJob(ctx context.Context, id int64) (CloudJob, error) {
	return scanCloudJob(s.db.QueryRowContext(ctx, `SELECT `+cloudJobColumns+` FROM cloud_jobs j JOIN cloud_plans p ON p.id=j.plan_id WHERE j.id=?`, id))
}

// NextCloudJob 由唯一工作者按入队顺序读取待执行任务。
func (s *Store) NextCloudJob(ctx context.Context) (CloudJob, error) {
	return scanCloudJob(s.db.QueryRowContext(ctx, `SELECT `+cloudJobColumns+` FROM cloud_jobs j JOIN cloud_plans p ON p.id=j.plan_id WHERE j.status='QUEUED' ORDER BY j.id LIMIT 1`))
}

// SetCloudJob 保存阶段、云证书标识和安全结果。
func (s *Store) SetCloudJob(ctx context.Context, j CloudJob) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cloud_jobs SET remote_certificate_id=?,status=?,stage=?,message=?,updated_at=? WHERE id=?`, j.RemoteCertificateID, j.Status, j.Stage, j.Message, nowText(), j.ID)
	return err
}

// RecoverCloudJobs 重启后重新接管中断任务，保留已上传的云证书标识和阶段。
func (s *Store) RecoverCloudJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cloud_jobs SET status='QUEUED',message='服务重启后恢复执行',updated_at=? WHERE status='RUNNING'`, nowText())
	return err
}

// CloudAutoNeeded 同一版本成功后不再自动执行，失败最多自动尝试三次且间隔五分钟。
func (s *Store) CloudAutoNeeded(ctx context.Context, p CloudPlan, certificateID int64) (bool, error) {
	var success, active, attempts int
	var latest sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='SUCCESS'),0),COALESCE(SUM(status IN ('QUEUED','RUNNING')),0),COUNT(*),MAX(updated_at) FROM cloud_jobs WHERE plan_id=? AND revision=? AND certificate_id=?`, p.ID, p.Revision, certificateID).Scan(&success, &active, &attempts, &latest)
	if err != nil {
		return false, err
	}
	return success == 0 && active == 0 && attempts < 3 && (!latest.Valid || time.Since(parseTime(latest.String)) >= 5*time.Minute), nil
}
