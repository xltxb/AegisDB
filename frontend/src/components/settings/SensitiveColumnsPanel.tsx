// 敏感字段 —— 按 表名 + 字段名 定义,命中的列在**结果离开网关之前**就被打码。
//
// 这个面板只维护规则。脱敏本身在服务端做,前端拿到的数据已经是打码后的:
// 让前端去打码等于把明文发到浏览器再请它别显示 —— 抓个包、开个 DevTools 就绕过了,
// 而且它已经躺在浏览器缓存和沿途任何代理的日志里。这条不是实现细节,是这个功能
// 全部的意义,所以面板上也把它写出来。
import { useImperativeHandle, useState, type HTMLAttributes, type Ref } from 'react'
import clsx from 'clsx'
import { EyeOff, Plus, Pencil, Trash2, X, Check, Search } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useI18n } from '@/hooks/useI18n'
import { useMountEffect } from '@/hooks/effects'
import type { SensitiveColumn } from '@/types'
import './SensitiveColumnsPanel.scoped.css'

type Style = SensitiveColumn['maskStyle']
const styles: Style[] = ['partial', 'full', 'hash']

export interface SensitiveColumnsPanelHandle { load: () => Promise<void> }
interface Props extends HTMLAttributes<HTMLDivElement> { ref?: Ref<SensitiveColumnsPanelHandle> }

export default function SensitiveColumnsPanel({ ref, className, ...rest }: Props) {
  const { t } = useI18n()
  const { notify, notifyError } = useUIStore.getState()
  const isAdmin = useAuthStore((s) => s.me?.roleCode === 'admin' || (s.me?.roleCodes || []).includes('admin'))

  const [rows, setRows] = useState<SensitiveColumn[]>([])

  // ---- 找得到,才谈得上维护 ----
  //
  // 这一块从卡片列表改成紧凑表格(2026-09):二十条规则在原来的版式里要滚两屏,而每行右边
  // 还空着一大片。真正的痛点不是好不好看,是**找不到** —— 想确认"phone 打没打码"只能拿眼
  // 睛顺着扫。所以先给搜索与两个筛选,再谈密度。
  const [kw, setKw] = useState('')
  const [styleFilter, setStyleFilter] = useState<'' | Style>('')
  const [stateFilter, setStateFilter] = useState<'' | 'on' | 'off'>('')
  function clearFilters() { setKw(''); setStyleFilter(''); setStateFilter('') }

  const offCount = rows.filter((r) => !r.enabled).length

  const q = kw.trim().toLowerCase()
  const shown = rows.filter((r) => {
    if (styleFilter && (r.maskStyle || 'partial') !== styleFilter) return false
    if (stateFilter === 'on' && !r.enabled) return false
    if (stateFilter === 'off' && r.enabled) return false
    if (!q) return true
    // 表名一起进搜索:规则多半写成 * ,但一旦有按表限定的,"这张表上都定了什么"是要问的。
    return `${r.tableName}.${r.columnName} ${r.note || ''}`.toLowerCase().includes(q)
  })
  const [editing, setEditing] = useState<{ id: number; tableName: string; columnName: string; maskStyle: Style; note: string; enabled: boolean } | null>(null)
  const edit = (p: Partial<NonNullable<typeof editing>>) => setEditing((v) => (v ? { ...v, ...p } : v))

  async function load() {
    try { setRows((await api.sensitiveColumns()) ?? []) } catch (e) { notifyError(e, t('loadFailed')) }
  }
  useMountEffect(() => { void load() })
  useImperativeHandle(ref, () => ({ load }))

  function openNew() {
    setEditing({ id: 0, tableName: '', columnName: '', maskStyle: 'partial', note: '', enabled: true })
  }
  function openEdit(r: SensitiveColumn) {
    setEditing({
      id: r.id, tableName: r.tableName, columnName: r.columnName,
      maskStyle: r.maskStyle || 'partial', note: r.note || '', enabled: r.enabled,
    })
  }

  async function save() {
    const f = editing
    if (!f) return
    if (!f.columnName.trim()) { notify(t('scNeedColumn'), 'error'); return }
    try {
      const env = await api.saveSensitiveColumn(f.id, {
        tableName: f.tableName.trim(), columnName: f.columnName.trim(),
        maskStyle: f.maskStyle, note: f.note.trim(), enabled: f.enabled,
      })
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      setEditing(null)
      await load()
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  // 停用/删除都会让该字段**从此明文回传**,所以两个动作都要先确认一次。
  async function toggle(r: SensitiveColumn) {
    if (!isAdmin) return
    if (r.enabled && !(await confirmAction(t('scDisableConfirm', { col: r.tableName + '.' + r.columnName })))) return
    try {
      const env = await api.saveSensitiveColumn(r.id, { ...r, enabled: !r.enabled })
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      await load()
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  async function remove(r: SensitiveColumn) {
    if (!isAdmin) return
    if (!(await confirmAction(t('scDelConfirm', { col: r.tableName + '.' + r.columnName })))) return
    try {
      const env = await api.deleteSensitiveColumn(r.id)
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      await load()
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  return (
    <div {...rest} className={clsx('sc', className)}>
      <div className="schead">
        <div className="scic"><EyeOff size={18} color="var(--accent-text)" /></div>
        <div className="grow">
          <div className="sct">{t('sensTitle')}</div>
          <div className="scs">{t('sensSub')}</div>
        </div>
        {isAdmin && <VButton variant="primary" height="34px" onClick={openNew}><Plus size={14} />{t('scNew')}</VButton>}
      </div>

      {/* 工具栏。条数、搜索、两个筛选 —— 二十条以上时这一行才是入口 */}
      {rows.length > 0 && (
        <div className="tools">
          <span className="cnt">
            {t('scCount', { n: rows.length })}{' '}
            {offCount > 0 && <span className="cntoff">{t('scCountOff', { n: offCount })}</span>}
          </span>
          <div className="sbox">
            <Search size={13} color="var(--text-faint)" />
            <input value={kw} onChange={(e) => setKw(e.target.value)} placeholder={t('scSearchPh')} spellCheck={false} />
            {kw && <button className="sx" onClick={() => setKw('')}><X size={12} /></button>}
          </div>
          <select value={styleFilter} onChange={(e) => setStyleFilter(e.target.value as '' | Style)} className="fsel">
            <option value="">{t('scFilterAllStyle')}</option>
            {styles.map((s2) => <option key={s2} value={s2}>{t('scStyle_' + s2)}</option>)}
          </select>
          <select value={stateFilter} onChange={(e) => setStateFilter(e.target.value as '' | 'on' | 'off')} className="fsel">
            <option value="">{t('scFilterAllState')}</option>
            <option value="on">{t('scOnlyOn')}</option>
            <option value="off">{t('scOnlyOff')}</option>
          </select>
        </div>
      )}

      {rows.length > 0 ? (
        <div className="tbl">
          <div className="th">
            <span>{t('scColHead')}</span>
            <span>{t('scStyleHead')}</span>
            <span>{t('scNoteHead')}</span>
            <span className="c">{t('scStateHead')}</span>
            <span />
          </div>
          {shown.map((r) => (
            <div key={r.id} className={clsx('tr', !r.enabled && 'off')}>
              <span className="colcell">
                {/* 表名是 * 时说"全部表":一个裸星号读起来像没填完,而它恰恰是最要紧的一档 */}
                <span className={clsx('tb', (!r.tableName || r.tableName === '*') && 'any')}>
                  {!r.tableName || r.tableName === '*' ? t('scAllTables') : r.tableName}
                </span>
                <span className="col">{r.columnName}</span>
                {!r.enabled && <span className="offtag">{t('scDisabledTag')}</span>}
              </span>
              {/* 三种方式三种颜色。它们的后果差别很大(抹光 / 留头尾 / 同值同码),
                  共用一个蓝胶囊等于把最该一眼分清的事藏起来 */}
              <span className="stcell">
                <span className={clsx('style', 's-' + (r.maskStyle || 'partial'))} title={t('scStyleHint_' + (r.maskStyle || 'partial'))}>
                  {t('scStyle_' + (r.maskStyle || 'partial'))}
                </span>
              </span>
              <span className="notecell" title={r.note}>{r.note || '—'}</span>
              <span className="c"><VSwitch value={r.enabled} disabled={!isAdmin} onChange={() => toggle(r)} /></span>
              <span className="acts">
                {isAdmin && (
                  <>
                    <button className="act" title={t('edit')} onClick={() => openEdit(r)}><Pencil size={13} /></button>
                    <button className="act del" title={t('delete')} onClick={() => remove(r)}><Trash2 size={13} /></button>
                  </>
                )}
              </span>
            </div>
          ))}
          {/* 筛没了和本来就没有,出口不同:前者该给一个清空条件的按钮 */}
          {!shown.length && (
            <div className="empty">
              {t('scNoMatch')}{' '}
              <button className="eclear" onClick={clearFilters}>{t('scClearFilters')}</button>
            </div>
          )}
        </div>
      ) : <div className="empty">{t('scEmpty')}</div>}

      {editing && (
        <div className="overlay">
          <div className="mask" onClick={() => setEditing(null)} />
          <div className="modal">
            <div className="mhead">
              <div className="mic"><EyeOff size={17} color="var(--accent-text)" /></div>
              <div><div className="mt">{editing.id ? t('scEdit') : t('scNew')}</div><div className="ms">{t('sensSub')}</div></div>
              <X size={17} className="mx" onClick={() => setEditing(null)} />
            </div>
            <div className="mbody">
              <div className="row2">
                <div><div className="fl">{t('scTable')}</div><input value={editing.tableName ?? ''} onChange={(e) => edit({ tableName: e.target.value })} className="in" placeholder={t('scTablePh')} /></div>
                <div><div className="fl">{t('scColumn')}</div><input value={editing.columnName ?? ''} onChange={(e) => edit({ columnName: e.target.value })} className="in" placeholder={t('scColumnPh')} /></div>
              </div>
              <div>
                <div className="fl">{t('scStyle')}</div>
                <select value={editing.maskStyle} onChange={(e) => edit({ maskStyle: e.target.value as Style })} className="in">
                  {styles.map((s) => <option key={s} value={s}>{t('scStyle_' + s)} — {t('scStyleHint_' + s)}</option>)}
                </select>
              </div>
              <div><div className="fl">{t('scNote')}</div><input value={editing.note} onChange={(e) => edit({ note: e.target.value })} className="in" placeholder={t('scNotePh')} /></div>
              <div className="hint">{t('scServerSideHint')}</div>
            </div>
            <div className="mfoot">
              <VButton onClick={() => setEditing(null)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" onClick={save}><Check size={14} />{t('btnSave')}</VButton>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
