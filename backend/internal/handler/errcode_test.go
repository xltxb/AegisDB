package handler

// 哨兵错误到信封 code 的映射,只该有一份。
//
// 从前有六份:compileErrCode / projectErrCode / metaErrCode / openErrCode /
// releaseErrCode / reviewErrCode。几乎逐字相同,而分叉恰恰落在要紧的地方 ——
//
//   · metaErrCode 用的是 `switch err` 裸比较,不是 errors.Is。五份拷贝里漏升级的
//     那一份:一旦有人在 service 那侧给错误裹上一层 %w 说明上下文,403 会悄悄降成
//     "参数错误",而两边都不会报错。
//   · 只有 openErrCode 认得 MFA 与"脚本路径未配置"。于是 pipeline.go 里有人在**一个**
//     handler 中内联补了 MFA 分支 —— 因为映射函数不认它。补丁打在撞见的那一处,
//     其余照旧。
//   · projectErrCode 两个分支返回同一个值,整个函数是空转的。
//
// 所以这组用例问的是包装过的哨兵:裸哨兵在两种写法下都对,**只有包装能把它们分开**。

import (
	"errors"
	"fmt"
	"testing"

	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

func TestErrCode_MapsSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"越权", service.ErrForbidden, resp.CodeForbidden},
		{"要二次验证", service.ErrMFARequired, resp.CodeMFARequired},
		{"二次验证码不对", service.ErrMFAInvalid, resp.CodeMFARequired},
		{"脚本路径未配置", service.ErrScriptPathUnset, resp.CodeScriptPathUnset},
		{"找不到", service.ErrNotFound, resp.CodeBadRequest},
		{"普通错误", errors.New("boom"), resp.CodeBadRequest},
	}
	for _, tc := range cases {
		if got := errCode(tc.err); got != tc.want {
			t.Errorf("%s:errCode = %d,want %d", tc.name, got, tc.want)
		}
		// 包装之后必须还是同一个答案 —— service 那侧随时可能加一层上下文说明,
		// 而那不该把一个权限拒绝变成参数错误。
		wrapped := fmt.Errorf("读取实例 %d 的元数据: %w", int64(7), tc.err)
		if got := errCode(wrapped); got != tc.want {
			t.Errorf("%s(包装后):errCode = %d,want %d", tc.name, got, tc.want)
		}
	}
}

func TestErrCode_NilIsNotAFailure(t *testing.T) {
	// 调用方只在 err != nil 时才问,但一个映射函数不该对 nil 给出"越权"这种答案。
	if got := errCode(nil); got != resp.CodeBadRequest {
		t.Errorf("errCode(nil) = %d", got)
	}
}
