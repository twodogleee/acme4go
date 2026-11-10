// 本文件提供受管理会话和 CSRF 保护的云部署 API，外部操作使用持久异步任务。
//
// @author Andersen
package control

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/store"
)

// cloudProducts 返回产品适配器能力描述。
func (s *Server) cloudProducts(w http.ResponseWriter, r *http.Request) {
	s.ok(w, s.services.CloudProducts(), "获取成功")
}

// cloudMutationID 区分新增和修改，不接受正文中的主键覆盖。
func cloudMutationID(r *http.Request) (int64, error) {
	if r.Method == http.MethodPost {
		return 0, nil
	}
	return pathID(r)
}

// listCloudCertificates 分页查询指定凭据的已有云证书。
func (s *Server) listCloudCertificates(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, app.BadRequest("页码格式无效"))
			return
		}
	}
	value, err := s.services.ListCloudCertificates(r.Context(), id, page, r.URL.Query().Get("search"))
	s.respond(w, value, "获取成功", err)
}

// listCloudPlans 返回上传规则与云产品目标。
func (s *Server) listCloudPlans(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.Store.ListCloudPlans(r.Context())
	s.respond(w, values, "获取成功", err)
}

// saveCloudPlan 保存通过适配器校验的规则。
func (s *Server) saveCloudPlan(w http.ResponseWriter, r *http.Request) {
	id, err := cloudMutationID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input app.CloudPlanInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.SaveCloudPlan(r.Context(), currentUser(r).ID, id, input)
	s.respond(w, value, "配置已保存", err)
}

// deleteCloudPlan 删除本地规则，保留云端资源。
func (s *Server) deleteCloudPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = s.services.DeleteCloudPlan(r.Context(), currentUser(r).ID, id)
	s.respond(w, nil, "配置已删除", err)
}

// runCloudPlan 将上传或部署加入持久队列，立即返回任务信息。
func (s *Server) runCloudPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.QueueCloudPlan(r.Context(), currentUser(r).ID, id)
	s.respond(w, value, "任务已入队", err)
}

// listCloudJobs 通过递减游标返回最近五十个任务。
func (s *Server) listCloudJobs(w http.ResponseWriter, r *http.Request) {
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeError(w, app.BadRequest("任务游标无效"))
			return
		}
		before = value
	}
	values, err := s.services.Store.ListCloudJobs(r.Context(), before)
	s.respond(w, values, "获取成功", err)
}

// cloudJobDetail 返回指定任务的最新执行结果。
func (s *Server) cloudJobDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.Store.FindCloudJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		err = app.NotFound("云任务不存在")
	}
	s.respond(w, value, "获取成功", err)
}
