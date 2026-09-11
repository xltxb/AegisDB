import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { EyeOff, Plus, Upload, Pencil, Trash2, Clock } from 'lucide-react'
import {
  useSensitiveColumns, useSaveSensitiveColumn, useDeleteSensitiveColumn,
  useImportSensitiveColumns,
} from '@/hooks/useGov'
import { useIsAdmin } from '@/hooks/usePermissions'
import { JIT_GRANTS_AVAILABLE } from '@/api/modules/gov'
import { Card, CardHead } from '@/components/common/Card'
import { Table, type Column } from '@/components/common/Table'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Switch } from '@/components/common/Switch'
import { Badge } from '@/components/common/Badge'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import { confirmAction } from '@/lib/confirm'
import type { SensitiveColumn } from '@/types'

type MaskStyle = SensitiveColumn['maskStyle']
const STYLES: MaskStyle[] = ['partial', 'full', 'hash']

/** 预览用的示例输入。它是**编出来的一串数字**,不是任何一行库里的数据。 */
const SAMPLE = '13800001234'

/**
 * 脱敏效果预览 —— 前端按方式渲染的**示例**字符串。
 *
 * 这一列容易被读成"这张表现在长什么样",所以说清楚:它不是真实数据,也没有经过
 * 任何一次查询。真实数据在**离开网关之前**就已经打好码了(gateway.MaskValue),
 * 前端拿到的一直是星号,手里从来没有原值 —— 没有原值,也就无从预览真的结果。
 * 这一列回答的只有一个问题:选这个方式之后,这一列会长成什么形状。
 *
 * 三个值都是把 gateway.MaskValue 的规则套在 SAMPLE 上算出来的,所以形状是真的:
 * partial 留头三尾四、full 固定六个星、hash 是 `#` 加 sha256 前十位
 * (sha256("13800001234") = 3941f6ec03…)。规则改了这里要跟着改,代价只是一个
 * 示例不像了,不影响任何判定。
 */
const MASK_PREVIEW: Record<MaskStyle, string> = {
  partial: '138****1234',
  full: '******',
  hash: '#3941f6ec03',
}

export default function GovPage() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const { data, isLoading, error, refetch } = useSensitiveColumns()
  const save = useSaveSensitiveColumn()
  const del = useDeleteSensitiveColumn()
  const [editing, setEditing] = useState<SensitiveColumn | null>(null)
  const [importing, setImporting] = useState(false)

  const rows = data ?? []

  function toggleExempt(r: SensitiveColumn, exempt: boolean) {
    // 豁免 = 这条规则不再生效 = **这一列从此明文回传**。方向朝"明文"走的那一下
    // 要先确认一次;关掉豁免(重新开始脱敏)不需要问。
    if (exempt && !confirmAction(t('govExemptConfirm', { col: `${r.tableName}.${r.columnName}` }))) return
    // PUT 是整行覆盖(service.SaveSensitiveColumn 把 enabled 直接写进去),
    // 所以要把原行带上,不能只发一个 enabled。
    save.mutate({ id: r.id, body: { ...r, enabled: !exempt } })
  }

  const columns: Column<SensitiveColumn>[] = [
    {
      key: 'col', head: t('govColField'), width: '1.6fr',
      cell: (r) => (
        <div>
          <div className="gov-colname">
            <span className="gov-dim">{r.tableName}</span>.{r.columnName}
          </div>
          {r.note && <div className="cell-sub">{r.note}</div>}
        </div>
      ),
    },
    {
      key: 'style', head: t('govColStyle'), width: '1fr',
      cell: (r) => <Badge tone="accent">{t(`govStyle_${r.maskStyle}`)}</Badge>,
    },
    {
      key: 'preview', head: t('govColPreview'), width: '1.6fr',
      cell: (r) => (
        <span className="gov-preview" title={t('govPreviewTip')}>
          <span className="gov-dim">{SAMPLE}</span>
          <span className="gov-arrow">→</span>
          {MASK_PREVIEW[r.maskStyle] ?? MASK_PREVIEW.partial}
        </span>
      ),
    },
    {
      key: 'exempt', head: t('govColExempt'), width: '1.2fr',
      cell: (r) => (
        <span className="gov-exempt">
          <Switch checked={!r.enabled} disabled={!isAdmin || save.isPending} onChange={(v) => toggleExempt(r, v)} />
          <span className="cell-sub">{t(r.enabled ? 'govMasking' : 'govExempted')}</span>
        </span>
      ),
    },
    {
      key: 'op', head: '', width: '96px',
      cell: (r) => (
        <div className="row-ops">
          {isAdmin && (
            <>
              <Button variant="ghost" title={t('govEdit')} onClick={() => setEditing(r)}>
                <Pencil size={14} />
              </Button>
              <Button
                variant="ghost"
                title={t('govDelete')}
                disabled={del.isPending}
                onClick={() => {
                  if (!confirmAction(t('govDeleteConfirm', { col: `${r.tableName}.${r.columnName}` }))) return
                  del.mutate(r.id)
                }}
              >
                <Trash2 size={14} />
              </Button>
            </>
          )}
        </div>
      ),
    },
  ]

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('govTitle')}</h1>
          <p>{t('govSub')}</p>
        </div>
        {isAdmin && (
          <div className="grow">
            <Button onClick={() => setImporting(true)}><Upload size={15} />{t('govImport')}</Button>
            <Button
              variant="primary"
              onClick={() => setEditing({
                id: 0, tableName: '', columnName: '', maskStyle: 'partial', enabled: true, note: '',
              })}
            >
              <Plus size={15} />{t('govNew')}
            </Button>
          </div>
        )}
      </header>

      <div className="notice">
        <EyeOff size={15} />
        {t('govServerSide')}
      </div>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && (
        <Table
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          empty={<Empty hint={t('govEmpty')} />}
        />
      )}

      <JitPanel />

      {editing && (
        <RuleModal
          rule={editing}
          busy={save.isPending}
          onClose={() => setEditing(null)}
          onSubmit={(body) => save.mutate({ id: editing.id, body }, { onSuccess: () => setEditing(null) })}
        />
      )}
      <ImportModal open={importing} onClose={() => setImporting(false)} />
    </div>
  )
}

/**
 * JIT 临时授权 —— 后端还没有这个能力。
 *
 * 路由表里没有任何对应的路径(见 api/modules/gov.ts 的 JIT_GRANTS_AVAILABLE),
 * 所以这里渲染的是一块「尚未接入」的空状态,而**不是**几行编出来的申请单。
 * 假数据在这一块格外危险:一张看着像真的待审列表会让人以为临时授权已经在跑了,
 * 于是不再去走真正的审批;而它其实谁也没授、谁也没批。
 */
function JitPanel() {
  const { t } = useTranslation()
  return (
    <Card className="gov-jit">
      <CardHead
        icon={<Clock size={16} />}
        title={t('govJitTitle')}
        sub={t('govJitSub')}
        actions={<Badge tone="warning">{t('govJitPlanned')}</Badge>}
      />
      <div className="gov-jitbody">
        <Empty hint={t(JIT_GRANTS_AVAILABLE ? 'govJitEmpty' : 'govJitNoBackend')} />
      </div>
    </Card>
  )
}

function RuleModal({
  rule, busy, onClose, onSubmit,
}: {
  rule: SensitiveColumn
  busy: boolean
  onClose: () => void
  onSubmit: (body: Partial<SensitiveColumn>) => void
}) {
  const { t } = useTranslation()
  const [f, setF] = useState<SensitiveColumn>(rule)
  const set = (patch: Partial<SensitiveColumn>) => setF((p) => ({ ...p, ...patch }))

  return (
    <Modal
      open
      title={rule.id ? t('govEditRule') : t('govNew')}
      sub={t('govRuleSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={busy || !f.columnName.trim()}
            onClick={() => onSubmit({
              tableName: f.tableName.trim(),
              columnName: f.columnName.trim(),
              maskStyle: f.maskStyle,
              note: f.note.trim(),
              enabled: f.enabled,
            })}
          >
            {t('save')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          {/* 留空 = 所有表。服务端把它存成 `*`,于是列表里看得见它是"所有表"
              而不是一个不知道什么意思的空格。 */}
          <label>{t('govTable')}</label>
          <input value={f.tableName} placeholder={t('govTablePh')} onChange={(e) => set({ tableName: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('govColumn')}</label>
          <input value={f.columnName} placeholder={t('govColumnPh')} onChange={(e) => set({ columnName: e.target.value })} />
        </div>
      </div>
      <div className="fld">
        <label>{t('govColStyle')}</label>
        <select value={f.maskStyle} onChange={(e) => set({ maskStyle: e.target.value as MaskStyle })}>
          {STYLES.map((s) => (
            <option key={s} value={s}>{t(`govStyle_${s}`)} — {t(`govStyleHint_${s}`)}</option>
          ))}
        </select>
      </div>
      <div className="fld">
        <label>{t('govNote')}</label>
        <input value={f.note} placeholder={t('govNotePh')} onChange={(e) => set({ note: e.target.value })} />
      </div>
      <div className="gov-previewbox">
        <span className="cell-sub">{t('govColPreview')}</span>
        <span className="gov-preview">
          <span className="gov-dim">{SAMPLE}</span>
          <span className="gov-arrow">→</span>
          {MASK_PREVIEW[f.maskStyle] ?? MASK_PREVIEW.partial}
        </span>
      </div>
      <div className="notice">{t('govServerSide')}</div>
    </Modal>
  )
}

/**
 * 把粘进来的几行变成几条规则。
 *
 * 格式:`表.字段` 或 `字段`(留空表名 = 所有表),可以再跟 `:方式`。没有 import
 * 接口,这里也不假装有 —— 它只是替人把 N 次「新增」点完,走的是同一条路由。
 */
function parseImport(text: string): Partial<SensitiveColumn>[] {
  const out: Partial<SensitiveColumn>[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const [target, style] = line.split(':')
    const dot = target.lastIndexOf('.')
    const tableName = dot > 0 ? target.slice(0, dot).trim() : ''
    const columnName = (dot > 0 ? target.slice(dot + 1) : target).trim()
    if (!columnName) continue
    const s = (style || '').trim() as MaskStyle
    out.push({
      tableName,
      columnName,
      maskStyle: STYLES.includes(s) ? s : 'partial',
      enabled: true,
      note: '',
    })
  }
  return out
}

function ImportModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const run = useImportSensitiveColumns()
  const [text, setText] = useState('')
  const parsed = parseImport(text)

  return (
    <Modal
      open={open}
      title={t('govImport')}
      sub={t('govImportSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={!parsed.length || run.isPending}
            onClick={() => run.mutate(parsed, { onSuccess: () => { setText(''); onClose() } })}
          >
            {t('govImportRun', { n: parsed.length })}
          </Button>
        </>
      }
    >
      <div className="fld">
        <label>{t('govImportPaste')}</label>
        <textarea
          value={text}
          placeholder={'tbl_user.phone:partial\ntbl_user.id_card:full\nemail:hash'}
          onChange={(e) => setText(e.target.value)}
        />
      </div>
      {/* 逐条建,遇到重复的那一条会被服务端顶回来 —— 这时前面几条已经建好了,
          所以提示里把"已建几条"说出来,而不是让人以为整批都没进去。 */}
      <div className="notice warn">{t('govImportNote')}</div>
    </Modal>
  )
}
