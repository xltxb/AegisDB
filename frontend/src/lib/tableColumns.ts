import type { Breakpoint } from '@/lib/breakpoints'

/**
 * 1 = 永远在场;2 = narrow 下收起;3 = mid 与 narrow 下都收起。
 *
 * **不写视同 1。** 9 张表里大部分列没有显式标注,把「没标注」读成最低优先级
 * 会让它们在窄屏集体消失 —— 而缺省应当是「保守地留着」。
 */
export type ColPriority = 1 | 2 | 3

export interface ColSpec {
  key: string
  /** grid 列宽,如 '1.5fr' / '96px'。 */
  width: string
  priority?: ColPriority
}

const CUTOFF: Record<Breakpoint, ColPriority> = { wide: 3, mid: 2, narrow: 1 }

/** 当前档位下该显示的列,顺序不变。 */
export function visibleCols<C extends ColSpec>(cols: C[], bp: Breakpoint): C[] {
  const max = CUTOFF[bp]
  return cols.filter((c) => (c.priority ?? 1) <= max)
}

/**
 * grid 模板。**必须喂 `visibleCols` 的结果**,不能喂原始列表 ——
 * 轨道数与单元格数一旦脱节,整张表错位一列,而这件事类型检查看不见。
 */
export function gridTemplate(cols: ColSpec[]): string {
  return cols.map((c) => c.width).join(' ')
}
