// 数据源页的新建 / 编辑 / 导入三个弹窗共用的表单件。
import api from '@/api'
import { engineLabels } from '@/lib/engines'
import { useI18n } from '@/hooks/useI18n'
import { useEnvTierStore } from '@/stores/envtier'

// Straight from the shared catalogue so the console can only offer engines the
// gateway can actually drive (see lib/engines). ClickHouse and Redis used to be
// listed here with no driver behind them: such a connection saves fine and then
// behaves as a simulated one.
export const engineOpts = engineLabels()

export interface ConnDraft {
  name: string; host: string; engine: string; envLabel: string
  username: string; password: string; database: string
}

export const blankDraft = (envLabel = ''): ConnDraft =>
  ({ name: '', host: '', engine: engineOpts[0], envLabel, username: '', password: '', database: '' })

// 保存之后探一下。回失败原因,'' = 通了。
//
// 新建、编辑、导入三处都要"存完探一次、连不上要说",只有汇报那一行不同 —— 以前三处各
// 写一份 try/catch,连不上的呈现方式也各不相同。
export async function probe(id: number, unreachable: string): Promise<string> {
  try { await api.testConnection(id); return '' } catch (e: any) { return e?.message || unreachable }
}

// The environments an instance can be placed in. This was the four built-in
// labels; it comes from the environment list now, so a cluster created in the
// tiers page is selectable here immediately, with no reload and no code change.
//
// Labels stay unique by appending the code when two environments share a display
// name — the select binds on the LABEL, so a duplicate would make one of them
// unpickable.
export function useEnvOpts() {
  useI18n() // 环境名按读者语言显示:切语言要重算
  const envtier = useEnvTierStore()
  const seen = new Map<string, number>()
  // 按读者语言的名字去重、显示:出厂环境在英文下是英文名(envtier.envLabel)。
  for (const e of envtier.environments) { const n = envtier.envLabel(e.code); seen.set(n, (seen.get(n) ?? 0) + 1) }
  const envOpts = envtier.environments.map((e) => {
    const n = envtier.envLabel(e.code)
    return { env: e.code as string, label: (seen.get(n) ?? 0) > 1 ? `${n} (${e.code})` : n }
  })
  /**
   * Options for editing ONE instance: the current list, plus the instance's own
   * environment when that no longer exists.
   *
   * Without the extra entry the select falls back to the first option, so opening
   * an instance whose environment was deleted and pressing save would quietly move
   * it into production. Surfacing the dangling code instead makes the operator pick
   * a destination deliberately.
   */
  function envOptsFor(code: string) {
    return envOpts.some((o) => o.env === code) ? envOpts : [...envOpts, { env: code, label: code }]
  }
  return { envOpts, envOptsFor }
}

// Oracle identifies the target DB by a service name (or a SID via the "sid/" prefix),
// not a plain schema name — hint that in the 数据库名 field placeholder.
export const dbHint = (engine: string, t: (k: string) => string) => (/oracle/i.test(engine) ? t('dbHintOracle') : 'orders_db')
