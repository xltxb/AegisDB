// 一张步骤卡。
//
// 页面把每个阶段画成一根长条表单的时候,看不出"这是一条按顺序执行的流水线"。这里
// 把它做成时间轴上的一个节点:左侧一条虚线把上下串起来,卡片自带序号环、类型图标与
// 类型徽标,头部是"这一步是什么",主体才是"这一步怎么配"。
//
// 数据所有权在页面:这里只读 stage,所有改动通过回调交回去 —— dirty 标记、审批人
// 成员的按需加载都是页面的事,卡片不该各自维护一份。
import { useState } from 'react'
import clsx from 'clsx'
import {
  ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Hourglass, Bell,
  ArrowUp, ArrowDown, Copy, Trash2, ChevronDown, Webhook, ListChecks, type LucideIcon,
} from 'lucide-react'
import VSelect from '@/components/common/VSelect'
import { useI18n } from '@/hooks/useI18n'
import type { PipelineStage, StageType } from '@/types'
import './StepCard.scoped.css'

interface Props {
  stage: PipelineStage
  index: number
  total: number
  readonly: boolean
  typeLabels: string[]
  roleLabels: string[]
  confirmRoleLabels: string[]
  /** 审批/确认节点当前选中的角色显示名。 */
  approverLabel: string
  /** 这个节点将来会找谁 —— 由页面解析好;empty 表示解析出来是空的(要告警)。 */
  who: { names: string[]; empty: boolean }
  /** 自动执行阶段:当前选的适用分层不允许自动执行时,给出的提示(保存会被后端拒)。 */
  autoWarn?: string
  onMove: (dir: -1 | 1) => void
  onRemove: () => void
  onDuplicate: () => void
  onChangeType: (label: string) => void
  onFailure: (value: 'abort' | 'continue') => void
  onCfg: (key: string, value: string) => void
  onApprover: (label: string) => void
}

const icon: Record<StageType, LucideIcon> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}

export default function StepCard({
  stage, index, total, readonly, typeLabels, roleLabels, confirmRoleLabels, approverLabel, who, autoWarn,
  onMove, onRemove, onDuplicate, onChangeType, onFailure, onCfg, onApprover, ...rest
}: Props) {
  const { t } = useI18n()
  // 徽标显示的是失败策略,不是类型:名字本身就是类型,再来一个同词的徽标不带任何信息。
  // onFailure 是后端真读的字段(见 service/pipeline.go),而表单从没露出过它。
  const stopsOnFail = (stage.onFailure || 'abort') !== 'continue'
  const [collapsed, setCollapsed] = useState(false)

  // 只读地取一个配置项;写入走 onCfg。
  function cfg(key: string, def = ''): string {
    try { return (JSON.parse(stage.config || '{}')[key] ?? def) as string } catch { return def }
  }
  const isFirst = index === 0
  const isLast = index === total - 1
  const Icon = icon[stage.type]

  const pickApprover = (v: string) => { if (!readonly) onApprover(v) }
  const people = (label: string) => (
    <>
      <span className="wl">{t(label, { n: who.names.length })}</span>
      {who.names.slice(0, 8).map((nm) => <span key={nm} className="pill">{nm}</span>)}
      {who.names.length > 8 && <span className="more">+{who.names.length - 8}</span>}
    </>
  )

  function body() {
    switch (stage.type) {
      // 规范审查:多严算不过
      case 'review': return (
        <div className="row">
          <span className="lbl">{t('plFailOn')}</span>
          <div className="seg">
            {['error', 'warn', 'none'].map((m) => (
              <button
                key={m} type="button" className={clsx('si', cfg('failOn', 'error') === m && 'active')} disabled={readonly}
                onClick={() => onCfg('failOn', m)}
              >{t('plFailOn_' + m)}</button>
            ))}
          </div>
        </div>
      )
      // 备份 / 校验:一段 SQL,等宽代码框
      case 'backup':
      case 'verify': return (
        <>
          <div className="row top">
            <span className="lbl">SQL</span>
            <textarea
              className="code" rows={3} spellCheck={false} disabled={readonly}
              value={cfg('sql')} placeholder={stage.type === 'backup' ? t('plBackupPh') : t('plVerifyPh')}
              onChange={(e) => onCfg('sql', e.target.value)}
            />
          </div>
          {/* 校验阶段一直读着 expect,表单却从没露出过它 —— 不知道这个键的人只能让校验
              永远"通过"。 */}
          {stage.type === 'verify' && (
            <div className="row">
              <span className="lbl">{t('plExpect')}</span>
              <div className="seg">
                {['', 'empty', 'nonempty'].map((m) => (
                  <button
                    key={m} type="button" className={clsx('si', cfg('expect', '') === m && 'active')} disabled={readonly}
                    onClick={() => onCfg('expect', m)}
                  >{t('plExpect_' + (m || 'any'))}</button>
                ))}
              </div>
            </div>
          )}
        </>
      )
      // 人工审批
      case 'approve': return (
        <>
          <div className="row">
            <span className="lbl">{t('plApprover')}</span>
            <VSelect value={approverLabel} options={roleLabels} height="32px" onChange={pickApprover} />
          </div>
          <div className={clsx('who', who.empty && 'warn')}>
            {who.empty ? t('plApproverEmpty') : people('plApproverWho')}
          </div>
        </>
      )
      // 人工确认 / 执行变更:都有一道"谁来点"的门
      case 'manual':
      case 'execute': return (
        <>
          {stage.type === 'manual' && (
            <div className="row">
              <span className="lbl">{t('plNote')}</span>
              <input
                className="in" disabled={readonly} value={cfg('note')} placeholder={t('plNotePh')}
                onChange={(e) => onCfg('note', e.target.value)}
              />
            </div>
          )}
          <div className="row">
            <span className="lbl">{t('plConfirmer')}</span>
            <VSelect value={approverLabel} options={confirmRoleLabels} height="32px" onChange={pickApprover} />
          </div>
          <div className={clsx('who', who.empty && 'warn')}>
            {who.empty ? t('plApproverEmpty')
              : !who.names.length ? t('plConfirmDefaultHint')
                : people('plConfirmWho')}
          </div>
        </>
      )
      // 自动执行:没有"谁来点"—— 前序阶段通过就下发。分层不允许时直接在卡片上说
      case 'auto_execute':
        return autoWarn ? <div className="who warn" data-testid="auto-exec-warn">{autoWarn}</div> : null
      // 结果通知:渠道由全局设置决定,这里只如实展示会走哪几条
      case 'notify': return (
        <div className="row">
          <span className="lbl">{t('plChannels')}</span>
          <span className="pill ch"><Webhook size={11} />Webhook</span>
          <span className="pill ch"><ListChecks size={11} />{t('plChannelAudit')}</span>
        </div>
      )
      default: return null
    }
  }

  return (
    <div {...rest} className={clsx('step', collapsed && 'collapsed')}>
      {/* 时间轴:序号环 + 连到下一张卡的虚线 */}
      <div className="rail">
        <div className="ring">{index + 1}</div>
        {!isLast && <div className="line" />}
      </div>

      <div className="card">
        <div className="head">
          <button type="button" className="fold" title={collapsed ? t('plExpand') : t('plCollapse')} onClick={() => setCollapsed(!collapsed)}>
            <ChevronDown size={14} className={clsx(collapsed && 'turned')} />
          </button>
          <span className="ic">{Icon && <Icon size={14} />}</span>
          {/* 名字 = 类型。它会进记录(快照 / 通知 / 审计),存的那份由服务端按类型给出
              规范值,这里只按读者的语言显示;换类型走右侧的下拉,不在名字上打字。 */}
          <span className="name">{t('plType_' + stage.type)}</span>
          <button
            type="button" className={clsx('badge', stopsOnFail ? 'stop' : 'go')} disabled={readonly}
            title={t('plOnFailTip')} onClick={() => onFailure(stopsOnFail ? 'continue' : 'abort')}
          >
            {stopsOnFail ? t('plOnFailAbort') : t('plOnFailContinue')}
          </button>
          <span className="grow" />
          {!readonly && <VSelect value={t('plType_' + stage.type)} options={typeLabels} height="28px" onChange={onChangeType} />}
          {!readonly && (
            <div className="acts">
              <button type="button" className="ab" disabled={isFirst} title={t('plMoveUp')} onClick={() => onMove(-1)}><ArrowUp size={13} /></button>
              <button type="button" className="ab" disabled={isLast} title={t('plMoveDown')} onClick={() => onMove(1)}><ArrowDown size={13} /></button>
              <button type="button" className="ab" title={t('plDuplicate')} onClick={onDuplicate}><Copy size={13} /></button>
              <button type="button" className="ab danger" title={t('delete')} onClick={onRemove}><Trash2 size={13} /></button>
            </div>
          )}
        </div>

        <div className="body" style={collapsed ? { display: 'none' } : undefined}>
          {body()}
          <div className="hint">{t('plHint_' + stage.type)}</div>
        </div>
      </div>
    </div>
  )
}
