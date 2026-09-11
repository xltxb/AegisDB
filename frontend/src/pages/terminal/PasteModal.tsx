import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { ShieldAlert } from 'lucide-react'
import { countStatements } from '@/lib/sqlCount'
import { useRiskCommands } from '@/hooks/useRiskRules'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'

/** 一条字典命中。level 是这台实例所在分层上的档位。 */
interface DictHit { command: string; level: string }

/**
 * 粘贴 SQL 的弹窗。
 *
 * 键盘直接粘进 xterm 也能跑,但那是**看不见**的:一批语句一边回显一边滚过去,
 * 人没有机会在下发到生产之前看一眼自己粘了什么。所以粘进来像是多条时就改走这里,
 * 先把全文摊开。确认之后走的还是同一条路(整批提交 → 服务端逐条判定逐条执行),
 * 这里改的只是"下发前先不先给人看一眼"。
 *
 * 下面那份高危命中是**界面上的提示**,不是裁决:真正判的是网关,它看的是完整语句
 * 而不是关键词。列出来是为了让人在按下执行之前知道这一批里有什么。
 */
export function PasteModal({
  open, text, env, inst, tierCode, onChange, onClose, onSubmit,
}: {
  open: boolean
  text: string
  env: string
  inst: string
  tierCode: string
  onChange: (v: string) => void
  onClose: () => void
  onSubmit: () => void
}) {
  const { t } = useTranslation()
  const { data: dict } = useRiskCommands()
  const ref = useRef<HTMLTextAreaElement | null>(null)

  // 打开就把光标放进文本框:这个弹窗存在的意义就是让人立刻能改、能读、能执行。
  useEffect(() => { if (open) ref.current?.focus() }, [open])

  if (!open) return null

  const n = countStatements(text)
  const upper = text.toUpperCase()
  const hits: DictHit[] = []
  for (const c of dict ?? []) {
    const level = c.tiers?.[tierCode as keyof typeof c.tiers]
    if (!level || level === 'off') continue
    // 字典里的命令是 `DROP TABLE` 这种词组,空白归一后做子串匹配即可 —— 它只是
    // 提示,宁可多列一条,也不要让人以为这一批是干净的。
    if (upper.replace(/\s+/g, ' ').includes(c.command.toUpperCase())) hits.push({ command: c.command, level })
  }

  return (
    <Modal
      open={open}
      width={720}
      title={t('pasteTitle')}
      sub={t('pasteDesc', { env, inst })}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" disabled={!text.trim()} onClick={onSubmit}>{t('pasteRun')}</Button>
        </>
      }
    >
      <textarea
        ref={ref}
        className="paste-box"
        value={text}
        spellCheck={false}
        placeholder={t('pastePh')}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); onSubmit() }
          else if (e.key === 'Escape') { e.preventDefault(); onClose() }
        }}
      />
      <div className="paste-foot">
        <span className="paste-count">{t('pasteCount', { n })}</span>
        <span className="paste-hint">{t('pasteHint')}</span>
      </div>

      {!!hits.length && (
        <div className="notice danger paste-hits">
          <ShieldAlert size={14} />
          <div>
            <div>{t('pasteRiskTitle', { n: hits.length })}</div>
            <div className="paste-hitlist">
              {hits.map((h) => (
                <span key={h.command} className={clsx('paste-hit', h.level)}>{h.command}</span>
              ))}
            </div>
            <div className="paste-hitnote">{t('pasteRiskNote')}</div>
          </div>
        </div>
      )}
    </Modal>
  )
}
