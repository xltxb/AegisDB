import { authApi } from './modules/auth'
import { terminalApi } from './modules/terminal'
import { connectionsApi } from './modules/connections'
import { envtierApi } from './modules/envtier'
import { permissionsApi } from './modules/permissions'
import { riskRulesApi } from './modules/riskRules'
import { reviewApi } from './modules/review'
import { pipelineApi } from './modules/pipeline'
import { approvalsApi } from './modules/approvals'
import { auditApi } from './modules/audit'
import { settingsApi } from './modules/settings'

/**
 * 各领域接口按模块拆在 `api/modules/`(前端开发文档 §02),这里只把它们并成一个
 * 扁平对象。调用方仍然写 `api.connections()`,与拆分前一字不差 —— 拆的是文件,
 * 不是调用面,所以这次重构不会在几十个页面里留下改动。
 */
export const api = {
  ...authApi,
  ...terminalApi,
  ...connectionsApi,
  ...envtierApi,
  ...permissionsApi,
  ...riskRulesApi,
  ...reviewApi,
  ...pipelineApi,
  ...approvalsApi,
  ...auditApi,
  ...settingsApi,
}

export default api
