// 本文件协调本机文件切换与部署期望状态变更，保证配置提交后没有旧任务继续物化。
//
// @author Andersen
package store

import "context"

// lockDeployment 等待本机部署协调权；等待期间可取消，取得后调用方必须释放。
func (s *Store) lockDeployment(ctx context.Context) error {
	select {
	case s.deploymentGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.unlockDeployment()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// unlockDeployment 释放本机部署协调权。
func (s *Store) unlockDeployment() { <-s.deploymentGate }

// WithLocalDeployment 在同一协调区间读取期望、物化并登记结果；回调不得再次修改目标、系列或完成订单。
// 当前项目只支持单控制平面；协调期间普通数据库查询、心跳和告警仍可执行。
func (s *Store) WithLocalDeployment(ctx context.Context, action func() error) error {
	if err := s.lockDeployment(ctx); err != nil {
		return err
	}
	defer s.unlockDeployment()
	return action()
}
