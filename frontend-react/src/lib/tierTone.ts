import type { BadgeTone } from '@/components/common/Badge'

/**
 * 分层色名 → 徽标色调。
 *
 * `lib/envTierLabels` 里的 `dotFor` / `dotForEnv` 回的是产品自己的色名(danger /
 * info / warning / success / muted),它是 Vue 版沿用下来的词汇;组件库这边只认
 * `BadgeTone`。映射写在一处,免得每页各判一次,然后出现同一个分层在两页不同颜色。
 *
 * `info` 落到 accent:两者在这套 token 里是同一件事 —— "值得一看,但不是警报"。
 */
export function toneOfDot(dot: string): BadgeTone {
  switch (dot) {
    case 'danger': return 'danger'
    case 'warning': return 'warning'
    case 'success': return 'success'
    case 'info': return 'accent'
    default: return 'neutral'
  }
}
