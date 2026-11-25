import { useRef, useState } from 'react'
import clsx from 'clsx'
import { ShieldAlert, ArrowRight, Radio } from 'lucide-react'
import VButton from '@/components/common/VButton'
import { useI18n } from '@/hooks/useI18n'
import { useAuthStore } from '@/stores/auth'
import type { Member } from '@/types'
import './ApprovalModal.scoped.css'

interface Props {
  open: boolean
  command: string
  instance: string
  env: string
  risk: 'high' | 'mid'
  auditId: string
  chain: Member[]
  rule: string
  onCancel: () => void
  onSubmit: (reason: string) => void
}

// 长 SQL 默认收起。
//
// 这个弹窗是一道**闸**:它存在的意义是让人填一个理由、然后点提交。而命令原文是这里唯一
// 长度不受控的东西 —— 一条几百列的 CREATE GLOBAL BINDING 能把它底下的环境、实例、审批链、
// 操作原因和提交按钮整个顶出屏幕。弹窗是居中定位再 overflow:hidden 的,顶出去的部分不是
// "滚动一下能看到",是被裁掉、永远够不着。
//
// 阈值按**行数**而不是字符数:一条 300 字符的单行 SQL 折行之后也就三四行,收起它反而碍事;
// 真正撑爆版面的是几十上百行的那种。
const CLAMP_CHARS = 360

export default function ApprovalModal({ open, ...rest }: Props) {
  // 每次打开都是一个新的内容组件:原因清空、提交锁解开、长命令重新收起(上一条的展开状态与这一条无关)。
  return open ? <Content {...rest} /> : null
}

function Content({ command, instance, env, risk, auditId, chain, rule, onCancel, onSubmit }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const me = useAuthStore((s) => s.me)

  const [reason, setReason] = useState('')
  const [expanded, setExpanded] = useState(false)
  const cmdChars = (command || '').length
  const longCmd = cmdChars > CLAMP_CHARS

  // 提交一次就锁住,直到下一次打开。必须**同步**置位:同一个事件循环里连点两下(双击、
  // 脚本连发)时,弹窗还没来得及关、按钮还没来得及禁用,第二下照样能再发一张单
  // (黑盒 2026-10 工单 05)。
  const locked = useRef(false)
  const [submitted, setSubmitted] = useState(false)
  function submit() {
    if (locked.current || !reason.trim()) return
    locked.current = true
    setSubmitted(true)
    onSubmit(reason)
  }

  const chainNodes = [
    ...(me ? [{ i: me.initials, name: me.name, isYou: true }] : []),
    ...(chain ?? []).map((m) => ({ i: m.initials, name: m.name, isYou: false })),
  ]

  const badge = risk === 'high'
    ? { t: t('apBadgeHigh'), bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
    : { t: t('apBadgePerm'), bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }

  return (
    <div className="appr-overlay">
      <div className="appr-mask" onClick={onCancel} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><ShieldAlert size={22} color="var(--danger)" /></div>
            <div className="grow">
              <div className="titlerow">
                <div className="title">{t('mApprTitle')}</div>
                <span className="badge" style={{ background: badge.bg, color: badge.c }}>{badge.t}</span>
              </div>
              <div className="desc">{t('mPolicyHit')} <span className="b">{rule || t('mPolicyName')}</span>{t('sentenceSep')}{t('mApprDesc')}</div>
            </div>
          </div>

          <div className={clsx('cmdbox', longCmd && !expanded && 'clamped')}>
            <div className="cl">{t('mPendCmd')}</div>
            <div className={clsx('cmd', longCmd && !expanded && 'clamp', longCmd && expanded && 'scroll')}>{command}</div>
            {longCmd && (
              <button className="cmd-more" onClick={() => setExpanded(!expanded)}>
                {expanded ? t('mCmdCollapse') : t('mCmdExpand', { n: cmdChars })}
              </button>
            )}
          </div>

          <div className="grid3">
            <div><div className="gl">{t('mEnv')}</div><div className="gv">{env?.toUpperCase()}</div></div>
            <div><div className="gl">{t('mInst')}</div><div className="gv">{instance}</div></div>
            <div><div className="gl">{t('mRisk')}</div><div className={clsx('gv', risk === 'high' ? 'danger' : 'warn')}>{risk === 'high' ? t('highTag') : t('med')}</div></div>
          </div>

          <div className="eyebrow">{t('mChain')}</div>
          <div className="chain">
            {chainNodes.map((p, i) => [
              i > 0 && <ArrowRight key={`a${i}`} size={14} color="var(--text-faint)" />,
              <div key={i} className={clsx('node', p.isYou && 'me')}>
                <div className={clsx('ava', p.isYou && 'me')}>{p.i}</div>
                <span>{p.isYou ? t('mYou') : p.name}</span>
              </div>,
            ])}
          </div>

          <div className="reason">
            <div className="gl">{t('mReason')} <span className="req">*</span></div>
            {/* 弹窗是在终端里按回车弹出来的:不把焦点拿过来,接着打的字(和中文输入法)全落在身后的终端上 */}
            <textarea autoFocus value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t('mReasonPh')} data-testid="appr-reason" />
          </div>
          <div className="lark"><Radio size={14} color="#2dcde6" />{t('mLarkNote1')} <span className="azure">Lark</span> {t('mLarkNote2')}</div>
        </div>
        <div className="footer">
          <div className="aud">{t('auditId')} · {auditId}</div>
          <div className="acts">
            <VButton variant="secondary" onClick={onCancel}>{t('mCancel')}</VButton>
            <VButton variant="primary" disabled={!reason.trim() || submitted} data-testid="appr-submit" onClick={submit}>{t('mSubmit')}</VButton>
          </div>
        </div>
      </div>
    </div>
  )
}
