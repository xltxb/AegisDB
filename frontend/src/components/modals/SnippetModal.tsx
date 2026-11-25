import { useRef, useState } from 'react'
import clsx from 'clsx'
import { Command, X, Plus, Pencil, Trash2, Keyboard, Play } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import { useSnippetStore } from '@/stores/snippets'
import { useUIStore } from '@/stores/ui'
import { SNIPPET_SLOTS, slotLabel, snippetPreview } from '@/lib/snippet'
import type { TerminalSnippet } from '@/types'
import './SnippetModal.scoped.css'

// Manage the operator's saved scripts and their hotkeys. Nothing here executes
// anything: the modal writes text, and pressing the key later submits that text
// through the terminal's normal path, where it is judged against whatever
// instance the session is on.

// onRun 把脚本交回终端去跑,**弹窗自己不执行任何东西**:那样会是第二条进网关的路,而判定
// 只长在第一条上。终端收到之后走的是和快捷键一模一样的那段 —— 敲进命令行、回车、过
// /risk/check、该开单开单、该要 MFA 要 MFA。
interface Props { open: boolean; onClose: () => void; onRun: (s: TerminalSnippet) => void }

export default function SnippetModal({ open, ...rest }: Props) {
  return open ? <Content {...rest} /> : null
}

function Content({ onClose, onRun }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const { items, limits, bySlot } = useSnippetStore()

  const [editing, setEditing, editingRef] = useStateRef<TerminalSnippet | null>(null)
  const [isNew, setIsNew] = useState(false)
  const [form, setForm] = useState({ name: '', body: '', slot: 0 })
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [err, setErr] = useState('')

  // The body cap is in bytes because the column is; a Chinese comment costs three
  // bytes a character, so a character counter would read "well under the limit"
  // on a body the server refuses.
  const bodyBytes = new TextEncoder().encode(form.body).length
  const overCap = bodyBytes > limits.maxBytes

  useMountEffect(() => { useSnippetStore.getState().load().catch(() => {}) })

  function closeEditor() {
    setEditing(null)
    setIsNew(false)
    setErr('')
  }

  function startNew() {
    setIsNew(true)
    setEditing(null)
    setErr('')
    setForm({ name: '', body: '', slot: firstFreeSlot() })
  }

  function startEdit(s: TerminalSnippet) {
    setIsNew(false)
    setEditing(s)
    setErr('')
    setForm({ name: s.name, body: s.body, slot: s.slot })
  }

  /** Suggest the lowest unused hotkey, or none when all nine are taken. */
  function firstFreeSlot(): number {
    return SNIPPET_SLOTS.find((n) => !bySlot[n]) ?? 0
  }

  // Which snippet currently holds the slot the form is about to claim. Saying so
  // up front matters: claiming a taken key releases the other one, and finding
  // that out afterwards means finding out that a key you rely on stopped working.
  const holder = form.slot ? bySlot[form.slot] : undefined
  const stealing = !holder || holder.id === editing?.id ? null : holder

  async function save() {
    if (savingRef.current) return
    setErr('')
    savingRef.current = true
    setSaving(true)
    try {
      const env = await api.snippetSave(editing?.id ?? 0, { ...form })
      if (env.code !== 0) { setErr(env.msg || t('snipSaveFail')); return }
      await useSnippetStore.getState().refresh()
      closeEditor()
    } catch (e: any) {
      setErr(e?.msg || t('snipSaveFail'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  // 点「执行」= 交回终端 + 关掉弹窗。
  //
  // 必须关:审批弹窗与 MFA 提示都会出现在终端那一层,弹窗留在上面会把它们整个盖住,
  // 看起来就像按下去没反应。而且人按下执行之后要看的本来就是结果,不是这张管理界面。
  function run(s: TerminalSnippet) {
    onRun(s)
  }

  const [confirmDel, setConfirmDel, confirmDelRef] = useStateRef(0)
  async function remove(s: TerminalSnippet) {
    if (confirmDelRef.current !== s.id) { setConfirmDel(s.id); return }
    setConfirmDel(0)
    try {
      await api.snippetDelete(s.id)
      await useSnippetStore.getState().refresh()
      if (editingRef.current?.id === s.id) closeEditor()
    } catch (e) {
      useUIStore.getState().notifyError(e, t('snipDelFail'))
    }
  }

  return (
    <div className="snip-overlay">
      <div className="snip-mask" onClick={onClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><Command size={21} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{t('snipTitle')}</div>
              <div className="desc">{t('snipSub')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>

          {/* The one thing about this feature that is not obvious from using it. */}
          <div className="note"><Keyboard size={14} /><span>{t('snipJudgeNote')}</span></div>
        </div>

        <div className="body">
          <div className="listcol">
            <div className="colhead">
              <span>{t('snipSaved', { n: items.length, max: limits.max })}</span>
              <span className={clsx('new', items.length >= limits.max && 'off')} onClick={() => { if (items.length < limits.max) startNew() }}><Plus size={12} />{t('snipNew')}</span>
            </div>
            {!items.length && <div className="empty">{t('snipEmpty')}</div>}
            {items.map((s) => (
              <div key={s.id} className={clsx('row', editing?.id === s.id && 'sel')} onClick={() => startEdit(s)}>
                <span className={clsx('key', !s.slot && 'none')}>{s.slot ? slotLabel(s.slot) : '—'}</span>
                <div className="rgrow">
                  <div className="rname">{s.name}</div>
                  <div className="rprev">{snippetPreview(s.body)}</div>
                </div>
                {/* 执行排在最前、样式最重:这是这一行最常被按的东西。没绑快捷键的脚本
                    此前根本没有办法跑 —— 这个按钮也是它们唯一的入口。 */}
                <span className="act run" title={t('snipRunTitle')} onClick={(e) => { e.stopPropagation(); run(s) }}><Play size={13} /></span>
                <span className="act" title={t('snipEdit')} onClick={(e) => { e.stopPropagation(); startEdit(s) }}><Pencil size={13} /></span>
                <span
                  className={clsx('act del', confirmDel === s.id && 'armed')}
                  title={confirmDel === s.id ? t('snipDelConfirm') : t('snipDel')}
                  onClick={(e) => { e.stopPropagation(); void remove(s) }}
                ><Trash2 size={13} /></span>
              </div>
            ))}
          </div>

          <div className="editcol">
            {isNew || editing ? (
              <>
                <label className="lbl">{t('snipName')}</label>
                <input value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} className="inp" maxLength={limits.maxName} placeholder={t('snipNamePh')} />

                <label className="lbl">{t('snipHotkey')}</label>
                <div className="slots">
                  <span className={clsx('slot', form.slot === 0 && 'on')} onClick={() => setForm((f) => ({ ...f, slot: 0 }))}>{t('snipNoKey')}</span>
                  {SNIPPET_SLOTS.map((n) => (
                    <span
                      key={n} className={clsx('slot', form.slot === n && 'on', !!bySlot[n] && bySlot[n].id !== editing?.id && 'taken')}
                      onClick={() => setForm((f) => ({ ...f, slot: n }))}
                    >{n}</span>
                  ))}
                </div>
                {stealing ? <div className="steal">{t('snipSteal', { name: stealing.name, key: slotLabel(form.slot) })}</div>
                  // 选了"不绑定"要说清它照样能用 —— 否则那一档读起来像"存了也跑不了"
                  : form.slot === 0 && <div className="nokey">{t('snipNoKeyHint')}</div>}

                <label className="lbl">
                  {t('snipBody')}{' '}
                  <span className={clsx('bytes', overCap && 'over')}>{bodyBytes} / {limits.maxBytes} B</span>
                </label>
                <textarea value={form.body} onChange={(e) => setForm((f) => ({ ...f, body: e.target.value }))} className="ta" spellCheck={false} placeholder={t('snipBodyPh')} />

                {err && <div className="err">{err}</div>}
                <div className="acts">
                  {/* 改完直接跑一遍是很自然的下一步,不必再回列表上找那一行 */}
                  {editing && <VButton variant="secondary" onClick={() => run(editing)}>{t('snipRun')}</VButton>}
                  <VButton variant="secondary" onClick={closeEditor}>{t('btnCancel')}</VButton>
                  <VButton variant="primary" disabled={saving || overCap || !form.name.trim() || !form.body.trim()} onClick={save}>
                    {t('snipSave')}
                  </VButton>
                </div>
              </>
            ) : (
              <div className="placeholder">
                <Command size={26} />
                <div>{t('snipPickOne')}</div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
