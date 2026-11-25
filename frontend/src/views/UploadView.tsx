import { useRef, useState, type ChangeEvent, type DragEvent } from 'react'
import clsx from 'clsx'
import { useAuthStore } from '@/stores/auth'
import { Upload, FileCode2, Download, Trash2, FolderCog } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { CODE_OK, CODE_SCRIPT_PATH_UNSET } from '@/api/http'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'
import type { ScriptUpload } from '@/types'
import './UploadView.scoped.css'

const kb = (n: number) => (n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB')
const goSettings = () => { void nav.push('/admin/settings') }

export default function UploadView() {
  const { t } = useI18n()
  const isAdmin = useAuthStore((s) => s.isAdmin)
  const [files, setFiles] = useState<ScriptUpload[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [needPath, setNeedPath] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  async function loadList() {
    try {
      const l = await api.scriptUploads()
      setFiles(Array.isArray(l) ? l : [])
    } catch { /* ignore */ }
  }
  useMountEffect(() => { void loadList() })

  async function uploadFile(file: File) {
    setBusy(true)
    setErr('')
    setNeedPath(false)
    try {
      const text = await file.text()
      const env = await api.scriptUpload(text, file.name)
      if (env.code === CODE_SCRIPT_PATH_UNSET) { setNeedPath(true); return }
      if (env.code === CODE_OK) { await loadList(); return }
      setErr(env.msg || t('uploadFailed'))
    } catch { setErr(t('uploadFailed')) } finally { setBusy(false) }
  }

  function onPick(e: ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0]
    if (f) void uploadFile(f)
    e.target.value = ''
  }
  function onDrop(e: DragEvent) {
    e.preventDefault()
    setDragOver(false)
    const f = e.dataTransfer?.files?.[0]
    if (f) void uploadFile(f)
  }

  async function download(u: ScriptUpload) {
    try {
      const blob = await api.scriptUploadDownload(u.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = u.filename
      a.click()
      URL.revokeObjectURL(url)
    } catch { setErr(t('downloadFailed')) }
  }
  async function remove(u: ScriptUpload) {
    // M15: 删除脚本文件前二次确认
    if (!(await confirmAction(t('upDelConfirm', { name: u.filename })))) return
    try { await api.scriptUploadDelete(u.id); await loadList() } catch { setErr(t('deleteFailed')) }
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">SCRIPT UPLOAD</div>
        </div>
      </div>
      <div className="col">
        <section className="card">
          <div className="shead">
            <div className="sic"><Upload size={18} color="var(--accent-text)" /></div>
            <div><div className="st">{t('uploadTitle')}</div><div className="ss">{t('uploadSub')}</div></div>
          </div>
          <div className="body">
            {needPath && (
              <div className="notice">
                <FolderCog size={15} /><span>{t('scPathTitle')}</span>
                {/* 系统设置在管理后台,只有平台管理员进得去;别人点了会被守卫挡回原页,看着像没反应(2026-10 审查) */}
                {isAdmin
                  ? <VButton variant="secondary" height="30px" onClick={goSettings}>{t('scPathGo')}</VButton>
                  : <span className="askadmin" data-testid="path-ask-admin">{t('scPathAskAdmin')}</span>}
              </div>
            )}

            <div
              className={clsx('drop', dragOver && 'over', busy && 'busy')}
              onClick={() => fileRef.current?.click()}
              onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
              onDragLeave={(e) => { e.preventDefault(); setDragOver(false) }} onDrop={onDrop}
            >
              <Upload size={24} />
              <div className="dt">{busy ? t('uploadRunning') : t('uploadDrop')}</div>
              <div className="dh">{t('uploadHint')}</div>
            </div>
            <input ref={fileRef} type="file" accept=".sql,text/plain" className="hidden" onChange={onPick} />
            {err && <div className="err">{err}</div>}
          </div>
        </section>

        <section className="card">
          <div className="shead">
            <div className="sic"><FileCode2 size={18} color="var(--accent-text)" /></div>
            <div><div className="st">{t('uploadList')}</div><div className="ss">{t('uploadListSub')}</div></div>
          </div>
          <div className="list">
            {!files.length && <div className="empty">{t('uploadNone')}</div>}
            {files.map((u) => (
              <div key={u.id} className="row">
                <FileCode2 size={16} className="ic" />
                <div className="grow">
                  <div className="fn">{u.filename}<span className={clsx('src', u.source)}>{u.source === 'terminal' ? t('uploadFromTerm') : t('uploadFromPage')}</span></div>
                  <div className="fm">{kb(u.size)} · {u.createdAt?.slice(5, 16)}</div>
                  <div className="fp" title={u.path}><span className="fpk">{t('uploadPath')}</span>{u.path}</div>
                </div>
                <button className="act" title={t('exportDownload')} onClick={() => download(u)}><Download size={15} /></button>
                <button className="act del" title={t('delete')} onClick={() => remove(u)}><Trash2 size={15} /></button>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  )
}
