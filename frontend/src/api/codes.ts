// Business result codes carried in the { code, msg, data } envelope.
//
// They live in their own side-effect-free module so pure logic that has to
// interpret them (see lib/execOutcome) can be imported and unit-tested without
// dragging in the axios client, which needs a bundler-provided import.meta.env.
// api/http re-exports them, so existing imports are unaffected.
export const CODE_OK = 0
// 未登录 / token 失效 / 会话被吊销。后端用 HTTP 200 + 这个码回(resp.Abort),不是 401。
export const CODE_UNAUTHORIZED = 40100
export const CODE_INTERCEPTED = 42200
export const CODE_SCRIPT_PATH_UNSET = 42600
export const CODE_EXPORT_PATH_UNSET = 42601
export const CODE_MFA_REQUIRED = 42800
// 这一步要重新出示当前登录密码(如绑定 MFA):弹窗据此亮出密码框
export const CODE_PASSWORD_REQUIRED = 42801
// 启用了 SSO,但请求里没有 SSO 登录态 —— 不是错误,登录页据此露出「使用公司 SSO 登录」
export const CODE_SSO_NO_SESSION = 40101
// SRD 认得这个人,但网关里没有给他开通账号 —— 登录页弹窗拦下,提示无权限访问
export const CODE_SSO_NO_ACCOUNT = 40302
// 来源 IP 不在管理后台白名单内(只出现在管理接口上):提示后退回用户后台
export const CODE_ADMIN_IP_BLOCKED = 40303
// 管理后台空闲超时(不是登出):退回用户后台,要人再点一次「管理后台」
export const CODE_ADMIN_IDLE = 40304
