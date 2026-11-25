// 项目 —— 数据库与升级单的归属。
//
// 它和标签(tags)是两回事,面板文案也刻意这么说:标签决定"谁能碰这个库"(访问
// 范围,判定层真的会看),项目决定"这个库归谁跟进"(组织维度,判定层不看)。两者
// 长得像,一旦被当成同一个东西用,总有一天会有人以为改了项目就改了权限。
import { useImperativeHandle, useState, type HTMLAttributes, type Ref } from 'react'
import clsx from 'clsx'
import { FolderKanban, Plus, Pencil, Trash2, X, Check, ArrowRight } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Project } from '@/types'
import './ProjectsPanel.scoped.css'

export interface ProjectsPanelHandle { load(): Promise<void> }

interface Props extends HTMLAttributes<HTMLDivElement> {
  // readonly:用户后台的项目页只看归属与跟进。项目的增删改是管理接口(IP 白名单、管理后台空闲超时都管它),
  // 只在管理后台 → 系统设置 → 项目里做;在用户后台点了会莫名其妙被 40303 / 40304 挡回来(2026-10 审查)。
  readonly?: boolean
  // 列表变了就喊一声 —— 别处(数据源行的归属下拉)拿的是同一份数据,
  // 不同步的话新建的项目要刷新页面才选得到。
  onChanged?: (projects: Project[]) => void
  ref?: Ref<ProjectsPanelHandle>
}

interface Editing { id: number; name: string; owner: string; description: string }

function editInAdmin() { nav.push({ path: '/admin/settings', query: { tab: 'projects' } }) }

export default function ProjectsPanel({ readonly = false, onChanged, ref, className, ...rest }: Props) {
  const { t } = useI18n()
  const me = useAuthStore((s) => s.me)
  const notify = useUIStore((s) => s.notify)
  const notifyError = useUIStore((s) => s.notifyError)
  const isAdmin = me?.roleCode === 'admin' || (me?.roleCodes || []).includes('admin')
  const canEdit = isAdmin && !readonly

  const [projects, setProjects] = useState<Project[]>([])
  const [editing, setEditing] = useState<Editing | null>(null)
  const patch = (p: Partial<Editing>) => setEditing((f) => f && { ...f, ...p })

  async function load() {
    try {
      const list = (await api.projects()) ?? []
      setProjects(list)
      onChanged?.(list)
    } catch (e) { notifyError(e, t('loadFailed')) }
  }
  useMountEffect(() => { void load() })
  useImperativeHandle(ref, () => ({ load }))

  function openNew() { setEditing({ id: 0, name: '', owner: '', description: '' }) }
  function openEdit(p: Project) {
    setEditing({ id: p.id, name: p.name, owner: p.owner || '', description: p.description || '' })
  }

  async function save() {
    const f = editing
    if (!f) return
    if (!f.name.trim()) { notify(t('prNameRequired'), 'error'); return }
    try {
      const env = f.id
        ? await api.updateProject(f.id, { name: f.name.trim(), owner: f.owner, description: f.description })
        : await api.createProject({ name: f.name.trim(), owner: f.owner, description: f.description })
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      setEditing(null)
      await load()
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  async function remove(p: Project) {
    // 名下还有库时后端会拒并说明剩几个 —— 这里不预判,让服务端的理由原样呈现。
    if (!(await confirmAction(t('prDelConfirm', { name: p.name })))) return
    try {
      const env = await api.deleteProject(p.id)
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      await load()
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  return (
    <div {...rest} className={clsx('pr', className)}>
      <div className="prhead">
        <div className="pric"><FolderKanban size={18} color="var(--accent-text)" /></div>
        <div className="grow">
          <div className="prt">{t('prTitle')}</div>
          <div className="prs">{t('prSub')}</div>
        </div>
        {canEdit ? <VButton variant="primary" height="34px" onClick={openNew}><Plus size={14} />{t('prNew')}</VButton>
          : isAdmin ? <VButton variant="secondary" height="34px" onClick={editInAdmin}>{t('prEditInAdmin')}<ArrowRight size={14} /></VButton>
            : null}
      </div>

      <div className="list">
        {projects.map((p) => (
          <div key={p.id} className="item">
            <div className="grow">
              <div className="itop">
                <span className="iname">{p.name}</span>
                {p.owner && <span className="iowner">{p.owner}</span>}
              </div>
              <div className="imeta">
                <span>{t('prDbs', { n: p.databases })}</span>
                <span className="sep">·</span>
                <span>{t('prReleases', { n: p.releases })}</span>
                {p.description && <><span className="sep">·</span><span className="dim">{p.description}</span></>}
              </div>
            </div>
            {canEdit && (
              <>
                <button className="act" title={t('edit')} onClick={() => openEdit(p)}><Pencil size={14} /></button>
                <button className="act del" title={t('delete')} onClick={() => remove(p)}><Trash2 size={14} /></button>
              </>
            )}
          </div>
        ))}
        {!projects.length && <div className="empty">{t('prEmpty')}</div>}
      </div>

      {editing && (
        <div className="overlay">
          <div className="mask" onClick={() => setEditing(null)} />
          <div className="modal">
            <div className="mhead">
              <div className="mic"><FolderKanban size={17} color="var(--accent-text)" /></div>
              <div><div className="mt">{editing.id ? t('prEdit') : t('prNew')}</div><div className="ms">{t('prSub')}</div></div>
              <X size={17} className="mx" onClick={() => setEditing(null)} />
            </div>
            <div className="mbody">
              <div><div className="fl">{t('prName')}</div><input value={editing.name} onChange={(e) => patch({ name: e.target.value })} className="in" placeholder={t('prNamePh')} /></div>
              <div><div className="fl">{t('prOwner')}</div><input value={editing.owner} onChange={(e) => patch({ owner: e.target.value })} className="in" placeholder={t('prOwnerPh')} /></div>
              <div><div className="fl">{t('prDesc')}</div><input value={editing.description} onChange={(e) => patch({ description: e.target.value })} className="in" placeholder={t('prDescPh')} /></div>
              <div className="hint">{t('prScopeHint')}</div>
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
