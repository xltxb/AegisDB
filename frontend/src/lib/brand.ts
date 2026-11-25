// 系统名称与 LOGO(品牌),管理员在「系统设置 → 品牌」里配置,存后端 tbl_setting(多活共享)。
//
// 这里只放状态与纯函数,不引 api / pinia:终端会话日志(lib/transcript.ts)也要读它,而单元测试
// 是在 Node 里直接跑 lib 的。拉取由 App.vue 在启动时做(GET /auth/branding,未登录也能读)。
//
// **没配置时各处保持原来的字样**:界面是 "DP DB GATEWAY",终端日志抬头是「Vela 数据库网关」。
// 所以默认值由调用方带(brandText 的 def);只要配了任意一个名称,各处就统一换成配置的名称。
import { reactive, ref } from 'vue'

export interface Branding {
  name: string
  nameEn: string
  logo: string
}

/** 界面上原来的系统名称:登录页、浏览器标题、设置页「关于」。 */
export const DEFAULT_BRAND = 'DP DB GATEWAY'

export const brand = reactive<Branding>({ name: '', nameEn: '', logo: '' })

export function setBrand(b: Partial<Branding> | null | undefined) {
  brand.name = (b?.name || '').trim()
  brand.nameEn = (b?.nameEn || '').trim()
  brand.logo = (b?.logo || '').trim()
}

/** 配了任意一个名称。 */
export function brandNamed(b: Branding = brand): boolean {
  return !!(b.name || b.nameEn)
}

/** 某种语言下该显示的名称:英文 英文名 → 中文名 → def;中文 中文名 → 英文名 → def。 */
export function brandText(lang: string, def: string = DEFAULT_BRAND, b: Branding = brand): string {
  const [first, second] = lang === 'en' ? [b.nameEn, b.name] : [b.name, b.nameEn]
  return first || second || def
}

// ---- 浏览器标题 ----
// 标题只由 App.vue 一处写:品牌名 + 布局给的待办数前缀。从前布局在挂载时把 document.title 抄下来
// 当底,品牌名后到就会被它盖回旧值。
export const titleCount = ref(0)

export function docTitle(base: string, n: number): string {
  return n > 0 ? `(${n}) ${base}` : base
}

// ---- LOGO 上传 ----
export const LOGO_MAX_BYTES = 256 * 1024
export const LOGO_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/x-icon', 'image/vnd.microsoft.icon', 'image/svg+xml']

/** 上传前的检查:'type' 不支持的类型、'size' 超过上限;通过返回 ''。服务端还会再校验一遍。 */
export function checkLogoFile(f: { type: string; size: number }): '' | 'type' | 'size' {
  if (!LOGO_TYPES.includes(f.type.toLowerCase())) return 'type'
  if (f.size > LOGO_MAX_BYTES) return 'size'
  return ''
}
