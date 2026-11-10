// 本文件编排只上传规则和产品部署目标，云 API 细节由适配器承担。
//
// @author Andersen
package app

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/twodogleee/acme4go/internal/store"
)

// CloudPlanInput 使用互斥证书来源描述独立上传和部署配置。
type CloudPlanInput struct {
	Name                string          `json:"name"`                // Name 是规则名称。
	CredentialID        int64           `json:"credentialId"`        // CredentialID 是共享凭据主键。
	Kind                string          `json:"kind"`                // Kind 是 UPLOAD 或 DEPLOY。
	Product             string          `json:"product"`             // Product 是产品适配器标识。
	SeriesID            *string         `json:"seriesId"`            // SeriesID 与 RemoteCertificateID 必须二选一。
	RemoteCertificateID string          `json:"remoteCertificateId"` // RemoteCertificateID 是所选已有云证书。
	Config              json.RawMessage `json:"config"`              // Config 由产品适配器负责规范化。
	AutoSync            bool            `json:"autoSync"`            // AutoSync 跟随本地证书系列新版本。
	Enabled             bool            `json:"enabled"`             // Enabled 控制执行开关。
}

// CloudProducts 返回稳定排序的产品能力描述。
func (s *Services) CloudProducts() []cloud.Product {
	values := []cloud.Product{}
	for _, adapter := range s.Cloud.Deployers {
		values = append(values, adapter.Product())
	}
	// 注册表顺序不应影响界面展示。
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}

// cloudCredential 只在调用边界解密已启用云部署用途的共享凭据。
func (s *Services) cloudCredential(ctx context.Context, id int64) (cloud.Credentials, error) {
	v, err := s.Store.FindCredential(ctx, id)
	if err != nil {
		return cloud.Credentials{}, normalizeStoreError(err, "云凭据不存在")
	}
	if !v.Enabled || !v.CloudEnabled || v.Provider != "ALIYUN" {
		return cloud.Credentials{}, BadRequest("凭据已停用或未启用云部署用途")
	}
	if err := s.decryptDNS(&v); err != nil {
		return cloud.Credentials{}, errors.New("云凭据解密失败")
	}
	return cloud.Credentials{AccessKeyID: v.AccessKeyID, AccessKeySecret: v.AccessKeySecret}, nil
}

// ListCloudCertificates 查询阿里云已有证书，不读取私钥；可独立用于上传后的查看和部署选择。
func (s *Services) ListCloudCertificates(ctx context.Context, credentialID int64, page int, keyword string) (cloud.CertificatePage, error) {
	if page < 1 || page > 10000 || len(keyword) > 253 {
		return cloud.CertificatePage{}, BadRequest("云证书查询参数无效")
	}
	credential, err := s.cloudCredential(ctx, credentialID)
	if err != nil {
		return cloud.CertificatePage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	value, err := s.Cloud.Catalog.List(ctx, credential, page, strings.TrimSpace(keyword))
	if err != nil {
		return value, &Error{Status: 502, Code: "CLOUD_QUERY_FAILED", Message: err.Error()}
	}
	return value, nil
}

// SaveCloudPlan 校验互斥来源和适配器配置，保存时不访问或修改云端。
func (s *Services) SaveCloudPlan(ctx context.Context, userID, id int64, input CloudPlanInput) (store.CloudPlan, error) {
	v := store.CloudPlan{ID: id, Name: strings.TrimSpace(input.Name), CredentialID: input.CredentialID, Kind: input.Kind, Product: input.Product, SeriesID: input.SeriesID, RemoteCertificateID: strings.TrimSpace(input.RemoteCertificateID), Config: input.Config, AutoSync: input.AutoSync, Enabled: input.Enabled}
	if v.Name == "" || len(v.Name) > 100 {
		return v, BadRequest("规则名称不能为空且最多 100 字节")
	}
	account, err := s.Store.FindCredential(ctx, v.CredentialID)
	if err != nil || account.Provider != "ALIYUN" || (v.Enabled && (!account.Enabled || !account.CloudEnabled)) {
		return v, BadRequest("请选择已启用云部署用途的凭据")
	}
	if v.SeriesID != nil {
		series, err := s.Store.FindCertificateSeries(ctx, *v.SeriesID, s.Config.RenewBefore)
		if err != nil || (v.Enabled && !series.Enabled) {
			return v, BadRequest("请选择已启用的证书系列")
		}
		if series.CADirectory == store.StagingDirectory {
			return v, BadRequest("测试 CA 证书不能上传到云产品")
		}
		if v.RemoteCertificateID != "" {
			return v, BadRequest("本地系列与已有云证书不能同时选择")
		}
	} else {
		remote, err := strconv.ParseUint(v.RemoteCertificateID, 10, 64)
		if err != nil || remote == 0 {
			return v, BadRequest("请选择已有阿里云证书")
		}
		if v.AutoSync {
			return v, BadRequest("固定云证书不支持跟随本地续期")
		}
	}
	switch v.Kind {
	case "UPLOAD":
		if v.SeriesID == nil || v.Product != "" {
			return v, BadRequest("上传规则必须选择本地证书系列且不指定云产品")
		}
		v.Config = json.RawMessage(`{}`)
	case "DEPLOY":
		adapter, ok := s.Cloud.Deployers[v.Product]
		if !ok {
			return v, BadRequest("不支持的云产品")
		}
		_, normalized, err := adapter.Validate(v.Config)
		if err != nil {
			return v, BadRequest(err.Error())
		}
		v.Config = normalized
	default:
		return v, BadRequest("规则类型必须是 UPLOAD 或 DEPLOY")
	}
	v.ID, err = s.Store.SaveCloudPlan(ctx, v)
	if err != nil {
		return v, Conflict("配置正在执行或相同凭据的目标已存在")
	}
	s.RecordAudit(ctx, &userID, "SAVE", "CLOUD_PLAN", formatID(v.ID), v.Name)
	s.wakeCloud()
	return s.Store.FindCloudPlan(ctx, v.ID)
}

// DeleteCloudPlan 只移除本地管理配置，不删除云证书或解除产品证书绑定。
func (s *Services) DeleteCloudPlan(ctx context.Context, userID, id int64) error {
	if err := s.Store.DeleteCloudPlan(ctx, id); err != nil {
		return Conflict("配置不存在或仍有执行中的任务")
	}
	s.RecordAudit(ctx, &userID, "DELETE", "CLOUD_PLAN", formatID(id), "删除配置，保留云端证书和绑定")
	return nil
}

// QueueCloudPlan 固定当前证书版本并立即返回持久任务，外部操作由后台完成。
func (s *Services) QueueCloudPlan(ctx context.Context, userID, id int64) (store.CloudJob, error) {
	p, err := s.Store.FindCloudPlan(ctx, id)
	if err != nil {
		return store.CloudJob{}, normalizeStoreError(err, "云配置不存在")
	}
	if !p.Enabled {
		return store.CloudJob{}, BadRequest("云配置已停用")
	}
	var certificateID *int64
	if p.SeriesID != nil {
		series, err := s.Store.FindCertificateSeries(ctx, *p.SeriesID, s.Config.RenewBefore)
		if err != nil || !series.Enabled {
			return store.CloudJob{}, BadRequest("证书系列已停用或不存在")
		}
		cert, err := s.Store.FindCurrentCertificate(ctx, *p.SeriesID, s.Config.RenewBefore)
		if err != nil {
			return store.CloudJob{}, BadRequest("证书系列尚无有效证书")
		}
		certificateID = &cert.ID
	}
	jobID, err := s.Store.EnqueueCloudJob(ctx, p, certificateID)
	if err != nil {
		return store.CloudJob{}, Conflict("已有任务执行中，或配置、凭据已变更")
	}
	s.RecordAudit(ctx, &userID, "EXECUTE", "CLOUD_PLAN", formatID(id), "云任务已入队")
	s.wakeCloud()
	return s.Store.FindCloudJob(ctx, jobID)
}
