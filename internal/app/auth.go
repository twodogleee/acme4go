// 本文件实现管理员密码、Cookie 会话和 CSRF 领域逻辑。
//
// @author Andersen
package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/store"
)

// fallbackPasswordHash 的初始化回调生成固定参数摘要，让不存在的用户名也执行同等 Argon2 校验。
var fallbackPasswordHash = func() string {
	hash, _ := cryptoutil.HashPassword(strings.Repeat("x", 32))
	return hash
}()

// LoginResult 是登录成功后写入 Cookie 与返回前端的会话信息。
type LoginResult struct {
	Token     string     `json:"-"`         // Token 是仅写入 HttpOnly Cookie 的会话令牌。
	CSRFToken string     `json:"csrfToken"` // CSRFToken 是状态变更请求需要回传的令牌。
	ExpiresAt time.Time  `json:"expiresAt"` // ExpiresAt 是会话过期时间。
	User      store.User `json:"user"`      // User 是当前登录管理员信息。
}

// Login 验证管理员密码并创建绑定 CSRF 令牌的服务端会话。
func (s *Services) Login(ctx context.Context, username, password string) (LoginResult, error) {
	if err := ValidateLoginInput(username, password); err != nil {
		return LoginResult{}, err
	}
	user, err := s.Store.FindUserByUsername(ctx, username)
	passwordHash := fallbackPasswordHash
	if err == nil {
		passwordHash = user.Password
	}
	validPassword := cryptoutil.VerifyPassword(passwordHash, password)
	if err != nil || !validPassword {
		return LoginResult{}, Unauthorized("用户名或密码错误")
	}
	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		return LoginResult{}, s.internalError("生成会话令牌失败", err)
	}
	csrf, err := cryptoutil.RandomToken(24)
	if err != nil {
		return LoginResult{}, s.internalError("生成 CSRF 令牌失败", err)
	}
	expiresAt := time.Now().Add(s.Config.SessionTTL)
	if err := s.Store.CreateSession(ctx, cryptoutil.TokenHash(token), cryptoutil.TokenHash(csrf), user.ID, passwordHash, expiresAt); err != nil {
		if errors.Is(err, store.ErrCredentialsChanged) {
			return LoginResult{}, Unauthorized("认证凭据已变化，请重新登录")
		}
		return LoginResult{}, s.internalError("创建会话失败", err)
	}
	_ = s.Store.TouchLogin(ctx, user.ID)
	s.RecordAudit(ctx, &user.ID, "LOGIN", "USER", "", "管理员登录")
	return LoginResult{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt.UTC(), User: user}, nil
}

// ValidateLoginInput 在缓存和高成本密码计算前限制认证字段的字节长度，不改变密码内容。
func ValidateLoginInput(username, password string) error {
	if len(username) == 0 || len(username) > cryptoutil.MaxUsernameBytes || len(password) == 0 || len(password) > cryptoutil.MaxPasswordBytes {
		return BadRequest("用户名须为 1 至 256 字节，密码须为 1 至 1024 字节")
	}
	return nil
}

// Authenticate 校验会话令牌并返回当前用户及服务端保存的 CSRF 哈希。
func (s *Services) Authenticate(ctx context.Context, token string) (store.User, string, error) {
	if token == "" {
		return store.User{}, "", Unauthorized("尚未登录")
	}
	user, csrfHash, err := s.Store.SessionUser(ctx, cryptoutil.TokenHash(token), time.Now())
	if err != nil {
		return store.User{}, "", Unauthorized("会话无效或已过期")
	}
	return user, csrfHash, nil
}

// VerifyCSRF 使用常量时间比较验证请求中的 CSRF 令牌。
func VerifyCSRF(expectedHash, token string) bool {
	if expectedHash == "" || token == "" {
		return false
	}
	actual := cryptoutil.TokenHash(token)
	return subtle.ConstantTimeCompare([]byte(expectedHash), []byte(actual)) == 1
}

// Logout 删除当前服务端会话；空令牌视为已经退出。
func (s *Services) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.Store.DeleteSession(ctx, cryptoutil.TokenHash(token))
}

// ChangePassword 校验旧密码、更新哈希并注销该用户的全部会话。
func (s *Services) ChangePassword(ctx context.Context, user store.User, currentPassword, newPassword string) error {
	stored, err := s.Store.FindUserByID(ctx, user.ID)
	if err != nil || !cryptoutil.VerifyPassword(stored.Password, currentPassword) {
		return BadRequest("当前密码错误")
	}
	hash, err := cryptoutil.HashPassword(newPassword)
	if err != nil {
		return BadRequest(err.Error())
	}
	if err := s.Store.UpdatePassword(ctx, user.ID, stored.Password, hash); err != nil {
		if errors.Is(err, store.ErrCredentialsChanged) {
			return Conflict("密码已被其他请求修改，请重新登录")
		}
		return s.internalError("更新管理员密码失败", err)
	}
	s.RecordAudit(ctx, &user.ID, "CHANGE_PASSWORD", "USER", "", "管理员修改密码并注销全部会话")
	return nil
}
