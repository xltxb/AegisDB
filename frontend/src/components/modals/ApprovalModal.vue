<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldAlert, ArrowRight, Radio } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import { useAuthStore } from '@/stores/auth'
import type { Member } from '@/types'

const props = defineProps<{
  open: boolean
  command: string
  instance: string
  env: string
  risk: 'high' | 'mid'
  auditId: string
  chain: Member[]
  rule: string
}>()
const emit = defineEmits<{ cancel: []; submit: [string] }>()

const { t } = useI18n()
const auth = useAuthStore()

const reason = ref('')

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
const expanded = ref(false)
const longCmd = computed(() => (props.command || '').length > CLAMP_CHARS)
const cmdChars = computed(() => (props.command || '').length)

// 提交一次就锁住,直到下一次打开。必须**同步**置位:同一个事件循环里连点两下(双击、
// 脚本连发)时,弹窗还没来得及关、按钮还没来得及禁用,第二下照样能再发一张单
// (黑盒 2026-10 工单 05)。
const submitted = ref(false)
function submit() {
  if (submitted.value || !reason.value.trim()) return
  submitted.value = true
  emit('submit', reason.value)
}

watch(() => props.open, (v) => {
  if (v) {
    submitted.value = false
    reason.value = ''
    expanded.value = false // 每次打开都重新收起:上一条的展开状态与这一条无关
  }
})

const chainNodes = computed(() => {
  const me = auth.me
  const head = me ? [{ i: me.initials, name: me.name, isYou: true }] : []
  const rest = props.chain.map((m) => ({ i: m.initials, name: m.name, isYou: false }))
  return [...head, ...rest]
})

const badge = () => (props.risk === 'high'
  ? { t: t('apBadgeHigh'), bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
  : { t: t('apBadgePerm'), bg: 'var(--warning-subtle)', c: 'var(--warning-text)' })
</script>

<template>
  <div v-if="open" class="appr-overlay">
    <div class="appr-mask" @click="emit('cancel')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><ShieldAlert :size="22" color="var(--danger)" /></div>
          <div class="grow">
            <div class="titlerow">
              <div class="title">{{ $t('mApprTitle') }}</div>
              <span class="badge" :style="{ background: badge().bg, color: badge().c }">{{ badge().t }}</span>
            </div>
            <div class="desc">{{ $t('mPolicyHit') }} <span class="b">{{ rule || $t('mPolicyName') }}</span>{{ $t('sentenceSep') }}{{ $t('mApprDesc') }}</div>
          </div>
        </div>

        <div class="cmdbox" :class="{ clamped: longCmd && !expanded }">
          <div class="cl">{{ $t('mPendCmd') }}</div>
          <div class="cmd" :class="{ clamp: longCmd && !expanded, scroll: longCmd && expanded }">{{ command }}</div>
          <button v-if="longCmd" class="cmd-more" @click="expanded = !expanded">
            {{ expanded ? $t('mCmdCollapse') : $t('mCmdExpand', { n: cmdChars }) }}
          </button>
        </div>

        <div class="grid3">
          <div><div class="gl">{{ $t('mEnv') }}</div><div class="gv">{{ env.toUpperCase() }}</div></div>
          <div><div class="gl">{{ $t('mInst') }}</div><div class="gv">{{ instance }}</div></div>
          <div><div class="gl">{{ $t('mRisk') }}</div><div class="gv" :class="risk === 'high' ? 'danger' : 'warn'">{{ risk === 'high' ? $t('highTag') : $t('med') }}</div></div>
        </div>

        <div class="eyebrow">{{ $t('mChain') }}</div>
        <div class="chain">
          <template v-for="(p, i) in chainNodes" :key="i">
            <ArrowRight v-if="i > 0" :size="14" color="var(--text-faint)" />
            <div class="node" :class="{ me: p.isYou }">
              <div class="ava" :class="{ me: p.isYou }">{{ p.i }}</div>
              <span>{{ p.isYou ? $t('mYou') : p.name }}</span>
            </div>
          </template>
        </div>

        <div class="reason">
          <div class="gl">{{ $t('mReason') }} <span class="req">*</span></div>
          <!-- 弹窗是在终端里按回车弹出来的:不把焦点拿过来,接着打的字(和中文输入法)全落在身后的终端上 -->
          <textarea v-model="reason" v-autofocus :placeholder="$t('mReasonPh')" data-testid="appr-reason" />
        </div>
        <div class="lark"><Radio :size="14" color="#2dcde6" />{{ $t('mLarkNote1') }} <span class="azure">Lark</span> {{ $t('mLarkNote2') }}</div>
      </div>
      <div class="footer">
        <div class="aud">{{ $t('auditId') }} · {{ auditId }}</div>
        <div class="acts">
          <VButton variant="secondary" @click="emit('cancel')">{{ $t('mCancel') }}</VButton>
          <VButton variant="primary" :disabled="!reason.trim() || submitted" data-testid="appr-submit" @click="submit">{{ $t('mSubmit') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.appr-overlay { position: fixed; inset: 0; z-index: 50; }
.appr-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 580px; max-width: 92vw; background: var(--surface-card);
  border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7), 0 0 0 1px rgba(45, 205, 230, 0.12);
  overflow: hidden; animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1);
  /* 纵向弹性 + 视口上限:内容再长也只让**正文**滚动,页脚那两个按钮永远钉在可见处。
     少了这一条,长 SQL 会把整个弹窗撑得比屏幕高,而它是居中定位的 —— 溢出的部分上下
     两头一起被裁掉,提交按钮就再也点不到了。 */
  display: flex; flex-direction: column; max-height: calc(100vh - 56px);
}
.topbar { height: 4px; background: linear-gradient(90deg, #f0473e, #ffb648); }
.pad { padding: 22px 26px 0; overflow-y: auto; flex: 1; min-height: 0; }
.hdr { display: flex; align-items: flex-start; gap: 14px; }
.ic { width: 42px; height: 42px; border-radius: 12px; background: var(--danger-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.titlerow { display: flex; align-items: center; gap: 10px; }
.title { font: 700 18px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; }
.badge { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); }
.desc { margin-top: 5px; font: 400 12.5px/1.55 var(--font-body); color: var(--text-muted); }
.desc .b { color: var(--text-body); }
.cmdbox { margin-top: 18px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); border-radius: 10px; padding: 12px 14px; }
.cl { font: 500 11px var(--font-mono); color: var(--text-faint); margin-bottom: 6px; }
.cmd { font: 500 13.5px var(--font-mono); color: var(--text-strong); word-break: break-all;
  white-space: pre-wrap; }
/* 收起态:给一个高度上限,底下用渐变告诉人"还有" —— 硬切一刀会让人以为 SQL 就这么长,
   而那正是他要签字确认的东西。 */
.cmd.clamp { max-height: 156px; overflow: hidden; }
.cmdbox.clamped { position: relative; }
.cmdbox.clamped::after {
  content: ''; position: absolute; left: 1px; right: 1px; bottom: 34px; height: 46px;
  background: linear-gradient(transparent, var(--surface-sunken)); pointer-events: none;
}
/* 展开态:自己滚,不去撑弹窗 —— 否则展开一条长 SQL 又把表单顶下去了。
   上限刻意收在 34vh:46vh 时这一格自己是够看了,但底下的「操作原因」正好被挤出可见区,
   于是人展开读完 SQL、抬头看见提交按钮是灰的,却不知道还差一个必填项 —— 要再往回滚
   一次才找得到。这个弹窗的任务是"确认 + 填理由 + 提交",读全文有展开-滚动,而表单必须
   一直在眼皮底下。 */
.cmd.scroll { max-height: 34vh; overflow-y: auto; }
.cmd-more { position: relative; z-index: 1; margin-top: 8px; padding: 3px 10px;
  font: 500 11px var(--font-mono); color: var(--text-muted); cursor: pointer;
  background: var(--surface-raised); border: 1px solid var(--border-subtle); border-radius: 999px; }
.cmd-more:hover { color: var(--text-strong); border-color: var(--border-default); }
.grid3 { margin-top: 14px; display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px; }
.gl { font: 500 11px var(--font-body); color: var(--text-faint); }
.gv { font: 600 12px var(--font-mono); color: var(--text-body); margin-top: 3px; }
.gv.danger { color: var(--danger-text); }
.gv.warn { color: var(--warning-text); }
.eyebrow { margin-top: 16px; font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-faint); text-transform: uppercase; }
.chain { margin-top: 10px; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.node { display: flex; align-items: center; gap: 7px; padding: 5px 11px 5px 5px; border: 1px solid var(--border-default); border-radius: 999px; }
.node.me { border-color: var(--accent-subtle-border); background: var(--accent-subtle); }
.node span { font: 600 11px var(--font-body); color: var(--text-body); }
.node.me span { color: var(--accent-text); }
.ava { width: 22px; height: 22px; border-radius: 50%; background: #232838; display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.ava.me { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; }
.reason { margin-top: 16px; }
.req { color: var(--danger-text); }
.reason textarea {
  width: 100%; box-sizing: border-box; min-height: 62px; resize: none; margin-top: 6px;
  border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken);
  padding: 11px 13px; font: 400 13px var(--font-body); color: var(--text-body); outline: none;
}
.lark { margin-top: 14px; display: flex; align-items: center; gap: 8px; font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.azure { color: #8facff; }
.footer { margin-top: 18px; flex-shrink: 0; display: flex; align-items: center; gap: 12px; padding: 14px 26px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.aud { font: 500 11px var(--font-mono); color: var(--text-faint); }
.acts { margin-left: auto; display: flex; gap: 10px; }
</style>
