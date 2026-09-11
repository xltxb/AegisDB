import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, FunctionSquare, Cog, Package, Zap, FileCode2 } from 'lucide-react'
import type { DbObjects } from '@/types'

const CATS = [
  { key: 'functions', label: 'treeFunctions', type: 'function', Icon: FunctionSquare },
  { key: 'procedures', label: 'treeProcedures', type: 'procedure', Icon: Cog },
  { key: 'packages', label: 'treePackages', type: 'package', Icon: Package },
  { key: 'triggers', label: 'treeTriggers', type: 'trigger', Icon: Zap },
] as const

/**
 * 树上一个库 / schema 下的可编程对象(函数、存储过程、包、触发器)。
 *
 * 纯展示:加载与源码窗口都归父级管,这里只画拿到的东西,并报告点了哪一个。
 * `objects === undefined` = 父级根本没请求过这个作用域(什么都不画);
 * `null` = 正在请求中。两者分开,是因为"还没问"和"问了还没回"给人的提示不一样。
 */
export function ObjectGroups({
  objects, onOpen,
}: { objects: DbObjects | null | undefined; onOpen: (type: string, name: string) => void }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState<Record<string, boolean>>({})

  if (objects === undefined) return null
  if (objects === null) return <div className="tv-hint">{t('treeLoading')}</div>

  const list = (key: string) => ((objects as unknown as Record<string, string[]>)[key] ?? [])

  return (
    <>
      {CATS.filter((c) => list(c.key).length).map((cat) => (
        <div key={cat.key}>
          <div className="tv-cat" onClick={() => setOpen((m) => ({ ...m, [cat.key]: !m[cat.key] }))}>
            {open[cat.key] ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
            <cat.Icon size={12} />
            {t(cat.label)}<span className="tv-cnt">{list(cat.key).length}</span>
          </div>
          {open[cat.key] && (
            <div className="tv-objs">
              {list(cat.key).map((o) => (
                <div key={o} className="tv-obj" title={t('objViewSource')} onClick={() => onOpen(cat.type, o)}>
                  <FileCode2 size={12} />{o}
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
      {objects.error && <div className="tv-hint err">{objects.error}</div>}
    </>
  )
}
