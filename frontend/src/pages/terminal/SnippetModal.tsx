import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { Plus, Trash2, ShieldAlert } from 'lucide-react'
import { SNIPPET_SLOTS, slotLabel, snippetPreview } from '@/lib/snippet'
import {
  FALLBACK_LIMITS, snippetsBySlot, useDeleteSnippet, useSaveSnippet, useSnippetLimits, useSnippets,
} from '@/hooks/useSnippets'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'
import type { TerminalSnippet } from '@/types'

/** 正在编辑的那一份。id 为 0 = 新建。 */
interface Draft { id: number; name: string; body: string; slot: number }
const EMPTY: Draft = { id: 0, name: '', body: '', slot: 0 }

/**
 * 快捷脚本的管理弹窗。
 *
 * 这里只管"存了什么、绑了哪个键"。按下快捷键之后会发生什么写在弹窗顶部那句话里,
 * 而且它是这一页最重要的一句:快捷键做的是**把脚本敲进命令行并回车**,判定按下键
 * 那一刻所连的实例来 —— 在开发环境写的脚本,在生产上按下去就是按生产的规则判。
 */
export function SnippetModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const { data } = useSnippets()
  const { data: limits } = useSnippetLimits()
  const save = useSaveSnippet()
  const del = useDeleteSnippet()
  const [draft, setDraft] = useState<Draft | null>(null)
  const [confirmDel, setConfirmDel] = useState(0)

  const list = (data ?? []) as TerminalSnippet[]
  const lim = limits ?? FALLBACK_LIMITS
  const bySlot = snippetsBySlot(list)

  // 这个槽位现在归谁。抢键不是错误,但得说在前面:原脚本会保留,只是解绑。
  const stealing = draft && draft.slot > 0 && bySlot[draft.slot] && bySlot[draft.slot].id !== draft.id
    ? bySlot[draft.slot]
    : null

  function pick(s: TerminalSnippet) {
    setConfirmDel(0)
    setDraft({ id: s.id, name: s.name, body: s.body, slot: s.slot })
  }

  function onDelete(id: number) {
    // 两段式:误点一下删不掉一份脚本,而第二下就在原地,不用再读一个弹窗。
    if (confirmDel !== id) { setConfirmDel(id); return }
    setConfirmDel(0)
    del.mutate(id)
    if (draft?.id === id) setDraft(null)
  }

  function submit() {
    if (!draft) return
    save.mutate(
      { id: draft.id, name: draft.name.trim(), body: draft.body, slot: draft.slot },
      { onSuccess: () => setDraft(null) },
    )
  }

  return (
    <Modal open={open} width={880} title={t('snipTitle')} sub={t('snipSub')} onClose={onClose}>
      <div className="notice warn"><ShieldAlert size={14} />{t('snipJudgeNote')}</div>

      <div className="snip-wrap">
        <div className="snip-list">
          <div className="snip-list-head">
            <span>{t('snipSaved', { n: list.length, max: lim.max })}</span>
            <Button variant="ghost" onClick={() => { setConfirmDel(0); setDraft({ ...EMPTY }) }}>
              <Plus size={13} />{t('snipNew')}
            </Button>
          </div>
          {!list.length && <div className="c-state">{t('snipEmpty')}</div>}
          {list.map((s) => (
            <div key={s.id} className={clsx('snip-item', draft?.id === s.id && 'on')} onClick={() => pick(s)}>
              <div className="snip-item-top">
                <span className="snip-name">{s.name}</span>
                {s.slot > 0 && <kbd className="snip-key">{slotLabel(s.slot)}</kbd>}
                <button
                  className={clsx('snip-del', confirmDel === s.id && 'armed')}
                  title={t('snipDel')}
                  onClick={(e) => { e.stopPropagation(); onDelete(s.id) }}
                >
                  {confirmDel === s.id ? t('snipDelConfirm') : <Trash2 size={12} />}
                </button>
              </div>
              <div className="snip-prev">{snippetPreview(s.body, 70)}</div>
            </div>
          ))}
        </div>

        <div className="snip-edit">
          {!draft ? <div className="c-state">{t('snipPickOne')}</div> : (
            <>
              <div className="fld">
                <label>{t('snipName')}</label>
                <input
                  value={draft.name}
                  maxLength={lim.maxName}
                  placeholder={t('snipNamePh')}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                />
              </div>
              <div className="fld">
                <label>{t('snipHotkey')}</label>
                <select value={draft.slot} onChange={(e) => setDraft({ ...draft, slot: Number(e.target.value) })}>
                  <option value={0}>{t('snipNoKey')}</option>
                  {SNIPPET_SLOTS.filter((n) => n <= lim.maxSlot).map((n) => (
                    <option key={n} value={n}>{slotLabel(n)}</option>
                  ))}
                </select>
              </div>
              {stealing && <div className="notice warn">{t('snipSteal', { key: slotLabel(draft.slot), name: stealing.name })}</div>}
              <div className="fld">
                <label>{t('snipBody')}</label>
                <textarea
                  className="snip-body"
                  value={draft.body}
                  spellCheck={false}
                  placeholder={t('snipBodyPh')}
                  onChange={(e) => setDraft({ ...draft, body: e.target.value })}
                />
                {/* 字节数而不是字符数:上限是服务端按字节算的,一条中文注释占三倍。 */}
                <div className="snip-bytes">{new TextEncoder().encode(draft.body).length} / {lim.maxBytes} B</div>
              </div>
              <div className="row-ops">
                <Button variant="ghost" onClick={() => setDraft(null)}>{t('cancel')}</Button>
                <Button variant="primary" disabled={!draft.name.trim() || !draft.body.trim() || save.isPending} onClick={submit}>
                  {t('snipSave')}
                </Button>
              </div>
            </>
          )}
        </div>
      </div>
    </Modal>
  )
}
