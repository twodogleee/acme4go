// 本文件验证阿里云 DNS 权限测试在取消后的记录清理。
//
// @author Andersen
package dnsprovider

import (
	"context"
	"testing"
)

// TestAliyunTestCleansRecordAfterCancellation 验证多域名测试超时后仍清理本项已创建记录。
func TestAliyunTestCleansRecordAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	// 在创建记录后取消请求，并验证删除使用独立上下文。
	err := testAliyunCredential(ctx, "example.com", func(callCtx context.Context, action map[string]string) (map[string]any, error) {
		calls++
		if calls == 1 {
			if action["DomainName"] != "example.com" || action["Action"] != "AddDomainRecord" {
				t.Fatal("未在指定根域名创建临时记录")
			}
			cancel()
			return map[string]any{"RecordId": "test-record"}, nil
		}
		if callCtx.Err() != nil || action["Action"] != "DeleteDomainRecord" || action["RecordId"] != "test-record" {
			t.Fatal("取消后没有独立清理上下文")
		}
		return map[string]any{}, nil
	})
	if err != nil || calls != 2 {
		t.Fatal("临时记录未清理", err)
	}
}
