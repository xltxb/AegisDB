// 指令统一在这里注册,main.ts 只认这一个入口。
import type { App } from 'vue'
import { vAutofocus } from './autofocus'
import { vPermission } from './permission'

export { vAutofocus, vPermission }

export function registerDirectives(app: App) {
  app.directive('autofocus', vAutofocus)
  app.directive('permission', vPermission)
}
