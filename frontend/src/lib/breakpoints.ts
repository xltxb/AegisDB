/**
 * 两个断点,不是八个。
 *
 * 改之前全站有 13 处 `@media`,散在 720/900/960/1040/1080/1100/1280/1500 八个值上。
 * 实测这些值分两簇:720-960 与 1040-1100 —— 也就是说它们本来就想表达两件事,
 * 只是每次有人加断点时各挑了一个手边的数。
 *
 * 边界取 1080 而不是 1280:现有 7 处折叠集中在 1040-1100,上推到 1280 会让
 * 1100-1280 屏宽的人失去今天已有的双栏密度。收敛是为了统一口径,不是降密度。
 */
export const BP_NARROW = 768
export const BP_MID = 1080

export type Breakpoint = 'wide' | 'mid' | 'narrow'

/** 视口宽 → 档位。非法值(挂载前读到的 0、NaN)退回最保守的一档,不抛错。 */
export function breakpointOf(width: number): Breakpoint {
  if (!Number.isFinite(width) || width <= BP_NARROW) return 'narrow'
  if (width <= BP_MID) return 'mid'
  return 'wide'
}
