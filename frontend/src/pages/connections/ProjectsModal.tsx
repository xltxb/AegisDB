import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FolderKanban, Pencil, Plus, Trash2 } from 'lucide-react'
import { useDeleteProject, useProjects, useSaveProject, type ProjectDraft } from '@/hooks/useProjects'
import { confirmAction } from '@/lib/confirm'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { Project } from '@/types'

const blank = (): ProjectDraft => ({ id: 0, name: '', owner: '', description: '' })

/**
 * 项目的增删改。
 *
 * 它和「库归属」是同一件事的两头,所以住在同一页上:在这里建一个项目,关掉抽屉
 * 就能在实例展开的库上把它挂过去,中间不用换页,也不用刷新(两处读的是同一个
 * query key)。
 *
 * 编辑用的是同一块面板里的内联表单,而不是再叠一层模态 —— 模态套模态之后,Esc
 * 关掉的是哪一层,按的人事先不知道。
 */
export function ProjectsModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useProjects()
  const save = useSaveProject()
  const del = useDeleteProject()
  const [draft, setDraft] = useState<ProjectDraft | null>(null)

  const projects: Project[] = data ?? []
  const set = (patch: Partial<ProjectDraft>) => setDraft((p) => (p ? { ...p, ...patch } : p))

  function remove(p: Project) {
    // 名下还有库时**服务端会拒**并说明还剩几个。这里不预判:那句理由要由真正做
    // 决定的一方给出,否则会出现"界面说能删、点下去失败"的错位。
    if (!confirmAction(t('prDelConfirm', { name: p.name }))) return
    del.mutate(p.id)
  }

  return (
    <Modal
      open
      title={t('prTitle')}
      sub={t('prSub')}
      width={680}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('close')}</Button>
          {!draft && (
            <Button variant="primary" onClick={() => setDraft(blank())}>
              <Plus size={15} />{t('prNew')}
            </Button>
          )}
        </>
      }
    >
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && !projects.length && !draft && <Empty hint={t('prEmpty')} />}

      {!isLoading && !error && projects.map((p) => (
        <div key={p.id} className="conn-pr">
          <div className="conn-pr-main">
            <div className="conn-pr-top">
              <span className="cell-strong">{p.name}</span>
              {p.owner && <Badge tone="accent">{p.owner}</Badge>}
            </div>
            <div className="cell-sub">
              {t('prDbs', { n: p.databases })} · {t('prReleases', { n: p.releases })}
              {p.description ? ` · ${p.description}` : ''}
            </div>
          </div>
          <Button
            variant="ghost"
            title={t('prEdit')}
            onClick={() => setDraft({
              id: p.id, name: p.name, owner: p.owner || '', description: p.description || '',
            })}
          >
            <Pencil size={14} />
          </Button>
          <Button
            variant="ghost"
            title={t('prDelete')}
            disabled={del.isPending}
            onClick={() => remove(p)}
          >
            <Trash2 size={14} />
          </Button>
        </div>
      ))}

      {draft && (
        <div className="conn-pr-form">
          <div className="conn-pr-form-head">
            <FolderKanban size={15} />
            {draft.id ? t('prEdit') : t('prNew')}
          </div>
          <div className="fld-2">
            <div className="fld">
              <label>{t('prName')}</label>
              <input
                value={draft.name}
                placeholder={t('prNamePh')}
                onChange={(e) => set({ name: e.target.value })}
              />
            </div>
            <div className="fld">
              <label>{t('prOwner')}</label>
              <input
                value={draft.owner}
                placeholder={t('prOwnerPh')}
                onChange={(e) => set({ owner: e.target.value })}
              />
            </div>
          </div>
          <div className="fld">
            <label>{t('prDesc')}</label>
            <input
              value={draft.description}
              placeholder={t('prDescPh')}
              onChange={(e) => set({ description: e.target.value })}
            />
          </div>
          {/* 项目不是安全边界。这一句留在表单里,而不是只写在文档中。 */}
          <p className="conn-pr-hint">{t('prScopeHint')}</p>
          <div className="row-ops">
            <Button variant="ghost" onClick={() => setDraft(null)}>{t('cancel')}</Button>
            <Button
              variant="primary"
              disabled={save.isPending || !draft.name.trim()}
              onClick={() => save.mutate(draft, { onSuccess: () => setDraft(null) })}
            >
              {t('save')}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  )
}
