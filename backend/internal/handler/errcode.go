package handler

// 哨兵错误到信封 code 的映射,全项目一份。
//
// 从前每个 handler 组各带一份自己的:compileErrCode / projectErrCode / metaErrCode /
// openErrCode / releaseErrCode / reviewErrCode。几乎逐字相同,而分叉恰恰落在要紧的
// 地方 ——
//
//   · metaErrCode 用的是 `switch err` 裸比较,不是 errors.Is。五份拷贝里漏升级的
//     那一份:一旦有人在 service 那侧给错误裹上一层 %w 说明上下文,403 会悄悄降成
//     "参数错误",而两边都不会报错,只有用户看见一句对不上号的提示。
//   · 只有 openErrCode 认得 MFA 与 ErrScriptPathUnset。于是 pipeline.go 里有人在
//     **一个** handler 中内联补了 MFA 分支(见 TriggerRelease)—— 补丁打在撞见的
//     那一处,其余几处照旧。
//   · projectErrCode 的两个分支返回同一个值,整个函数是空转的。
//
// 合成一份之后,认得的哨兵对每一条通道都一样。这比"各自只认自己用得着的"更严谨:
// 一个 handler 今天不会看见 MFA,不等于它明天调的 service 不会开始返回。
//
// ErrNotFound 仍映射到 CodeBadRequest,与合并前的全部六份一致 —— 这是个既有的
// API 约定,不在这次整理的范围里改。

import (
	"errors"

	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

func errCode(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return resp.CodeForbidden
	case errors.Is(err, service.ErrMFARequired), errors.Is(err, service.ErrMFAInvalid):
		return resp.CodeMFARequired
	case errors.Is(err, service.ErrScriptPathUnset):
		return resp.CodeScriptPathUnset
	default:
		return resp.CodeBadRequest
	}
}
