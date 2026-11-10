// 本文件提供订单执行详情、告警投递管理和列表筛选参数校验。
//
// @author Andersen
package control

import (
	"errors"
	"net/http"
	"strings"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/store"
)

// resourceListFilter 只接受页面支持的状态和有界文本，数据库层负责参数化查询。
func resourceListFilter(r *http.Request, statuses ...string) (store.ListFilter, error) {
	filter := store.ListFilter{Keyword: strings.TrimSpace(r.URL.Query().Get("search")), Status: r.URL.Query().Get("status"), SeriesID: strings.TrimSpace(r.URL.Query().Get("seriesId"))}
	if len([]rune(filter.Keyword)) > 200 || len(filter.SeriesID) > 128 {
		return filter, app.BadRequest("搜索条件过长")
	}
	if filter.Status == "" || filter.Status == "ALL" {
		return filter, nil
	}
	for _, status := range statuses {
		if status == filter.Status {
			return filter, nil
		}
	}
	return filter, app.BadRequest("状态筛选值无效")
}

// orderDetail 返回订单快照和脱敏阶段时间线。
func (s *Server) orderDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	order, err := s.services.Store.FindOrder(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, app.NotFound("证书订单不存在"))
		return
	}
	if err != nil {
		s.respond(w, nil, "获取失败", err)
		return
	}
	events, err := s.services.Store.ListOrderEvents(r.Context(), id)
	s.respond(w, map[string]any{"order": order, "events": events}, "获取成功", err)
}

// listAlerts 返回按投递状态筛选的历史和待发送通知。
func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	page, pageSize, err := listPageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	filter, err := resourceListFilter(r, "PENDING", "SENDING", "RETRY", "SENT", "FAILED")
	if err != nil {
		writeError(w, err)
		return
	}
	values, total, err := s.services.Store.ListAlertDeliveries(r.Context(), page, pageSize, filter.Status)
	s.respond(w, listPage{Items: values, Total: total, Page: page, PageSize: pageSize}, "获取成功", err)
}

// testAlert 创建测试事件并明确告知用户需要查看实际投递结果。
func (s *Server) testAlert(w http.ResponseWriter, r *http.Request) {
	err := s.services.TestAlert(r.Context(), currentUser(r).ID)
	s.respond(w, nil, "测试通知已入队，请在投递记录中查看结果", err)
}

// retryAlert 重新发送失败通知，鉴权和 CSRF 由管理路由中间件统一处理。
func (s *Server) retryAlert(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = s.services.RetryAlert(r.Context(), currentUser(r).ID, id)
	s.respond(w, nil, "通知已重新入队", err)
}
