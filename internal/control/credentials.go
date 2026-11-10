// 本文件提供统一凭据入口，权限验证仍按 DNS 和云部署各自的操作契约执行。
//
// @author Andersen
package control

import (
	"net/http"

	"github.com/twodogleee/acme4go/internal/app"
)

// listCredentials 返回所有用途的脱敏凭据。
func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.ListCredentials(r.Context())
	s.respond(w, values, "获取成功", err)
}

// saveCredential 创建或修改共享凭据，正文不接受持久主键覆盖。
func (s *Server) saveCredential(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.Method != http.MethodPost {
		value, err := pathID(r)
		if err != nil {
			writeError(w, err)
			return
		}
		id = value
	}
	var input app.CredentialInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.SaveCredential(r.Context(), currentUser(r).ID, id, input)
	s.respond(w, value, "凭据已保存", err)
}

// deleteCredential 只允许删除未被任何用途引用的凭据。
func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = s.services.DeleteCredential(r.Context(), currentUser(r).ID, id)
	s.respond(w, nil, "凭据已删除", err)
}
