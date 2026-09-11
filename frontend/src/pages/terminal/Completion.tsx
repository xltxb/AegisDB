import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { Table2, Terminal as TerminalIcon } from 'lucide-react'

export type AcKind = 'meta' | 'table'
export interface AcItem { label: string; kind: AcKind }

/**
 * 输入时向下弹出的候选。
 *
 * 只补两样东西:元命令(`\dt` 这些),和**已经探查到**的表名。没探查过的实例不在
 * 候选里 —— 补全不该成为一个会偷偷去连生产库的功能(理由同 DbTree 的搜索)。
 *
 * 位置由调用方算好后传进来:xterm 把文字画在 canvas 上,光标不是一个可以定位的
 * DOM 节点,只能按行高把它换算成像素。
 */
export function Completion({
  items, index, top, onPick,
}: { items: AcItem[]; index: number; top: number; onPick: (i: number) => void }) {
  const { t } = useTranslation()
  if (!items.length) return null
  return (
    <div className="ac" style={{ top }}>
      {items.map((it, i) => (
        <div
          key={it.kind + it.label}
          className={clsx('ac-item', i === index && 'on')}
          // mousedown 而不是 click:click 之前焦点已经离开终端,补全采纳后光标就
          // 不在命令行上了,下一个字符会敲进空处。
          onMouseDown={(e) => { e.preventDefault(); onPick(i) }}
        >
          {it.kind === 'table' ? <Table2 size={13} /> : <TerminalIcon size={13} />}
          <span className="ac-label">{it.label}</span>
          <span className="ac-kind">{t(it.kind === 'table' ? 'acTable' : 'acMeta')}</span>
        </div>
      ))}
      <div className="ac-hint">{t('acHint')}</div>
    </div>
  )
}

/** 终端自己的反斜杠命令。补全里只出现命令本身,参数由人接着敲。 */
export const META_COMMANDS = [
  '\\?', '\\conns', '\\c', '\\clear', '\\l', '\\dt', '\\dv', '\\dn', '\\di',
  '\\du', '\\ds', '\\df', '\\d', '\\x', '\\conninfo',
]
