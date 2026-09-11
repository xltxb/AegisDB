// 业务码单独成模块,不带副作用 —— 纯逻辑(见 lib/execOutcome)要解释这些码,
// 而 api/http 需要 bundler 提供的 import.meta.env,单测里拉不进来。
export const CODE_OK = 0
export const CODE_INTERCEPTED = 42200
export const CODE_SCRIPT_PATH_UNSET = 42600
export const CODE_EXPORT_PATH_UNSET = 42601
export const CODE_MFA_REQUIRED = 42800
export const CODE_LOGIN_THROTTLED = 42900
export const CODE_FORBIDDEN = 40300
export const CODE_IP_NOT_ALLOWED = 40301
