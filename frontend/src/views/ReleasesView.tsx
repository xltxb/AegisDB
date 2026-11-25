// 数据库变更发布 (CI/CD) — 发布单列表 + 流水线状态。
//
// 这里管列表、筛选、轮询与"摊开的是哪一张";详情那一栏在 ReleaseDetailPane,
// 新建弹窗在 ReleaseForm(三个文件共用同一份样式作用域)。
import { useRef, useState } from 'react'
import clsx from 'clsx'
import { Plus, X, Search, RefreshCw } from 'lucide-react'
import VButton from '@/components/common/VButton'
import ReleaseDetailPane, { executorText, useEnvTone } from './ReleaseDetailPane'
import ReleaseForm from './ReleaseForm'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { releaseExecutor } from '@/lib/releaseExecutor'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useI18n } from '@/hooks/useI18n'
import { useRoute } from '@/hooks/useNav'
import { useMountEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import type { Project, Connection, Pipeline, Release, ReleaseDetail, ReleaseStage } from '@/types'
import './ReleasesView.scoped.css'

export default function ReleasesView() {
  const { t } = useI18n()
  const ui = useUIStore.getState() // 只用动作,不读状态
  const envTone = useEnvTone()

  const [releases, setReleases, releasesRef] = useStateRef<Release[]>([])
  const [openId, setOpenId, openIdRef] = useStateRef(0)
  // 能看全部发布单的人(L2 及以上、审计员 —— 后端 canSeeAllReleases 说了算)一进来就是「全部」
  // (2026-10-30 用户确认);其他人只有「我的」。
  const canSeeAll = useAuthStore((s) => !!s.me?.canSeeAllReleases)
  const [scope, setScope, scopeRef] = useStateRef<'mine' | 'all'>(canSeeAll ? 'all' : 'mine')
  // 按项目跟进升级单 —— 这正是项目这个维度存在的理由。0 = 全部项目。
  const route = useRoute()
  const routeRef = useRef(route)
  routeRef.current = route
  const [projectFilter, setProjectFilter, projectFilterRef] = useStateRef(0)
  const [projects, setProjects] = useState<Project[]>([])
  // 按单号 / 环境找单。都在后端筛:列表只取最新 50 张,前端自己过滤会漏掉更早的单。
  //
  // 输入框里的字(kwText)和拿去查的字(keyword)是两个:输入法拼写过程中只动前一个,
  // 不会拿半截拼音去查。
  const [kwText, setKwText] = useState('')
  const [keyword, setKeyword, keywordRef] = useStateRef('')
  const [envFilter, setEnvFilter, envFilterRef] = useStateRef('') // environment 编码(fat 等),不是分层;'' = 全部环境
  const [envs, setEnvs] = useState<string[]>([])
  // 选项来自后端(当前范围和项目下落过的环境)。切了范围 / 项目之后选中的那个可能不在里面,
  // 也要留着 —— 否则下拉显示成空白,人却还在按它筛。
  const envOptions = envFilter && !envs.includes(envFilter) ? [...envs, envFilter].sort() : envs
  const filtering = !!keyword.trim() || !!envFilter
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [conns, setConns] = useState<Connection[]>([])
  const [openStage, setOpenStageState, openStageRef] = useStateRef(0)

  // ---- 脚本 / 日志查看器 ----
  // 选中阶段就是想看它干了什么:自动切到日志;取消选中回到脚本。手动点的 tab 不被覆盖,
  // 直到下一次换阶段。
  const [codeTab, setCodeTab] = useState<'sql' | 'log'>('sql')
  function setOpenStage(id: number) {
    if (id === openStageRef.current) return
    setOpenStageState(id)
    setCodeTab(id ? 'log' : 'sql')
  }

  // 选中那张单的详情单独存一份。列表接口故意不带阶段日志 / 审查结果(体积,见后端
  // ListReleases),所以列表一刷新(轮询、切「我的 / 全部」、换项目)就会把摊开的那张盖成
  // "该阶段暂无日志" —— 用户在 REL-1 上看到的正是这个。摊开的单只从详情取内容,列表只决定
  // 它还在不在当前范围里。
  const [detail, setDetail] = useState<ReleaseDetail | null>(null)
  const inList = releases.find((r) => r.id === openId)
  const open: ReleaseDetail | null = !inList ? null : detail?.id === inList.id ? detail : inList
  // A run is "live" while it can still change without anyone touching THIS page.
  // `waiting` counts: the decision arrives through the approval queue (or the 飞书
  // card, or the external callback) and the server-side sweeper resumes the run —
  // so a page that stopped polling there would sit on "等待处理" long after the
  // pipeline had finished.
  const anyLive = () =>
    releasesRef.current.some((r) => r.status === 'pending' || r.status === 'running' || r.status === 'waiting')

  // 请求编号:边打字边筛,慢的旧响应不能把新结果盖掉。
  const listSeq = useRef(0)
  // 筛选条件变了:等到第一份新结果回来,再决定摊开的那张还留不留(见 loadList)。
  const reselectAfterLoad = useRef(false)
  async function loadList() {
    const seq = ++listSeq.current
    try {
      const page = await api.releases(scopeRef.current, '', 1, 50, projectFilterRef.current, keywordRef.current.trim(), envFilterRef.current)
      if (seq !== listSeq.current) return
      setReleases(page.items ?? [])
      setEnvs(page.envs || [])
      const items = releasesRef.current
      if (reselectAfterLoad.current) {
        reselectAfterLoad.current = false
        // 摊开的那张被筛掉了就换成结果里的第一张,没结果就空着。别留一张列表里没有的单。
        if (!items.some((r) => r.id === openIdRef.current)) {
          setOpenId(items[0]?.id || 0)
          setOpenStage(0)
          setDetail(null)
          void refreshOpen()
        }
      }
      if (!openIdRef.current && items.length) setOpenId(items[0].id)
      ui.setPageSub({ key: 'rlSub2', params: { n: page.total } })
    } catch (e) { ui.notifyError(e, t('loadFailed')) }
  }
  function applyFilters() {
    reselectAfterLoad.current = true
    return loadList()
  }
  // 关键字:停手再查。定时器在改关键字的同一刻设下 —— 清除 / 回车紧接着取消它,不能多查一次。
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  function cancelSearchTimer() {
    if (searchTimer.current) { clearTimeout(searchTimer.current); searchTimer.current = null }
  }
  function changeKeyword(v: string) {
    if (v === keywordRef.current) return
    setKeyword(v)
    cancelSearchTimer()
    searchTimer.current = setTimeout(() => { searchTimer.current = null; void applyFilters() }, 300)
  }
  function searchNow() {
    cancelSearchTimer()
    void applyFilters()
  }
  function clearKeyword() {
    setKwText('')
    setKeyword('')
    searchNow()
  }
  // 每次请求编号:先点 A 再点 B,A 的响应晚到也不能把 B 的详情换掉。
  const openSeq = useRef(0)
  async function refreshOpen() {
    if (!openIdRef.current) return
    const seq = ++openSeq.current
    try {
      const r = await api.release(openIdRef.current)
      if (seq !== openSeq.current || r.id !== openIdRef.current) return
      setDetail(r)
      setReleases((list) => list.map((x) => (x.id === r.id ? r : x)))
    } catch { /* the list keeps the last known state */ }
  }
  function select(r: Release) {
    setOpenId(r.id)
    setOpenStage(0)
    setCodeTab('sql') // 换了单先看它要改什么;openStage 本来就是 0 时上一行不会动它
    void refreshOpen()
  }

  // 手动刷新:列表和摊开那张的详情一起重拿,选中的单和选中的阶段都不动。
  const [refreshing, setRefreshing, refreshingRef] = useStateRef(false)
  async function refreshAll() {
    if (refreshingRef.current) return
    setRefreshing(true)
    try { await Promise.all([loadList(), refreshOpen()]) }
    finally { setRefreshing(false) }
  }

  // 轮询:上一轮(列表 + 详情)没回来就跳过这一拍。
  // 不加这道闸,后端一慢(2026-11-10 本地 SQLite 单连接被压住),每 2.5 秒照发一轮,
  // 请求在后端排队越堆越长,连登录都排在后面等了 1 分多钟 —— 页面自己把后端拖死。
  const polling = useRef(false)
  async function pollTick() {
    if (polling.current || !anyLive()) return
    polling.current = true
    try { await Promise.all([loadList(), refreshOpen()]) }
    finally { polling.current = false }
  }

  // 下面这些函数只读 ref 和引用不变的 set,挂在计时器上不会过期。
  useMountEffect(() => {
    let timer: ReturnType<typeof setInterval> | null = null
    let gone = false
    void (async () => {
      useEnvTierStore.getState().load().catch(() => { /* 胶囊退回中性色 */ })
      try { setConns((await api.connections()) ?? []) } catch { /* the form falls back to an empty picker */ }
      try { setPipelines((await api.pipelines()) ?? []) } catch { /* same */ }
      try { setProjects((await api.projects()) ?? []) } catch { /* 筛选器降级为“全部项目” */ }
      // 从「项目」页点进来时带着 ?project=<id>:直接落到筛过的列表,而不是让人
      // 到了这页再从下拉里把刚才点的那个项目重新找一遍。
      const fromProject = Number(routeRef.current.query.project)
      if (fromProject > 0) setProjectFilter(fromProject)
      // 右上角「待办」里点一条「等你确认」过来时带着 ?rel=<id>:直接把那张单摊开。
      // 在 loadList 之前设,好让它不被"默认选中第一条"盖掉。
      const fromTodo = Number(routeRef.current.query.rel)
      if (fromTodo > 0) setOpenId(fromTodo)
      await loadList()
      // 那张单不一定在当前范围/当前页里 —— 确认人常常不是发起人,看不到全部的人默认范围是"我的"。
      // 直接问服务端要这一张,而不是在已加载的列表里找:找不到的表现是页面看起来坏了。
      if (fromTodo > 0 && !releasesRef.current.some((r) => r.id === fromTodo)) {
        try {
          const r = await api.release(fromTodo)
          setReleases((list) => [r, ...list])
        } catch { ui.notify(t('rlNotFound', { rel: fromTodo }), 'info') }
      }
      await refreshOpen()
      if (!gone) timer = setInterval(pollTick, 2500)
    })()
    return () => { gone = true; if (timer) clearInterval(timer); cancelSearchTimer() }
  })

  const executorOf = (r: Release) => executorText(releaseExecutor(r.stages), t)

  function toggleStage(id: number) {
    setOpenStage(openStageRef.current === id ? 0 : id)
  }

  // ---- actions ----
  async function abort(r: Release) {
    if (!(await confirmAction(t('rlAbortConfirm', { no: r.relNo })))) return
    try {
      const env = await api.abortRelease(r.id)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      await loadList(); await refreshOpen()
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }
  async function continueStage(r: Release, st: ReleaseStage) {
    try {
      const env = await api.continueStage(r.id, st.id)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      ui.notify(t('rlContinued'), 'success')
      setTimeout(() => { void loadList(); void refreshOpen() }, 400)
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  // ---- create form ----
  const [formOpen, setFormOpen] = useState(false)
  async function onCreated(id: number | undefined) {
    setFormOpen(false)
    // 刚提的单不一定在当前的单号 / 环境筛选里,而提完要看的就是它。
    setKwText('')
    setKeyword('')
    setEnvFilter('')
    cancelSearchTimer()
    await loadList()
    if (id) { setOpenId(id); await refreshOpen() }
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">RELEASE PIPELINE</div>
          <div className="v-sub">{t('rlSub')}</div>
        </div>
        <div className="v-head-acts">
          <div className="rsearch">
            <Search size={13} className="rsicon" />
            <input
              value={kwText} className="rsinput" type="text" maxLength={128}
              placeholder={t('rlSearchPh')} aria-label={t('rlSearchPh')}
              // onInput 而不是 onChange:后者会吞掉"值是脚本直接写进去的"那种 input 事件(自动填充、扩展)
              onInput={(e) => {
                const v = e.currentTarget.value
                setKwText(v)
                if (!(e.nativeEvent as InputEvent).isComposing) changeKeyword(v)
              }}
              onCompositionEnd={(e) => changeKeyword(e.currentTarget.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') searchNow() }}
            />
            {keyword && (
              <button type="button" className="rsclear" title={t('rlSearchClear')} aria-label={t('rlSearchClear')} onClick={clearKeyword}>
                <X size={12} />
              </button>
            )}
          </div>
          {canSeeAll && (
            <div className="seg small">
              <div className={clsx('si', scope === 'mine' && 'active')} onClick={() => { setScope('mine'); void applyFilters() }}>{t('rlMine')}</div>
              <div className={clsx('si', scope === 'all' && 'active')} onClick={() => { setScope('all'); void applyFilters() }}>{t('rlAll')}</div>
            </div>
          )}
          <div className="pfilter">
            <select value={projectFilter} className="psel" onChange={(e) => { setProjectFilter(Number(e.target.value)); void applyFilters() }}>
              <option value={0}>{t('rlAllProjects')}</option>
              {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </div>
          <div className="pfilter">
            <select value={envFilter} className="psel rlenv" aria-label={t('rlAllEnvs')} onChange={(e) => { setEnvFilter(e.target.value); void applyFilters() }}>
              <option value="">{t('rlAllEnvs')}</option>
              {envOptions.map((e) => <option key={e} value={e}>{e}</option>)}
            </select>
          </div>
          <VButton className="rl-refresh" disabled={refreshing} title={t('rlRefresh')} onClick={refreshAll}>
            <RefreshCw size={14} className={clsx(refreshing && 'spin')} />{refreshing ? t('rlRefreshing') : t('rlRefresh')}
          </VButton>
          <VButton variant="primary" onClick={() => setFormOpen(true)}><Plus size={15} />{t('rlNew')}</VButton>
        </div>
      </div>

      <div className="wrap">
        {/* 左栏:发布单卡片列表 */}
        <div className="list">
          {releases.map((r) => {
            const who = executorOf(r)
            return (
              <div
                key={r.id} className={clsx('ritem', r.id === openId && 'on')}
                role="button" tabIndex={0} onClick={() => select(r)} onKeyDown={(e) => { if (e.key === 'Enter') select(r) }}
              >
                <div className="rtop">
                  <span className="rno">{r.relNo}</span>
                  {r.changeType && <span className={clsx('ctb', r.changeType)}>{r.changeType.toUpperCase()}</span>}
                  {r.projectName && <span className="prj">{r.projectName}</span>}
                  <span className={clsx('pill', r.status)}>{t('rlSt_' + r.status)}</span>
                </div>
                <div className="rtitle">{r.title}</div>
                {/* 在哪儿改:环境、实例、库。库是这张单真正动的东西,不再压成灰字。 */}
                <div className="rmeta rwhere">
                  <span className={clsx('tag env', envTone(r.tierCode))} title={t('rlKEnv')}>{r.env || '—'}</span>
                  <span className="rinst">{r.instance}</span>
                  {r.database && <span className="rdb" title={t('rlKDb')}>/ {r.database}</span>}
                </div>
                {/* 从哪儿来:上游单号带标签,一眼和本单号 REL-xx 分开;再是来源系统、发起人。 */}
                <div className="rmeta rfrom">
                  {r.externalRef && <span className="xref" title={t('rlKRef')}><span className="xk">{t('rlKRefShort')}</span>{r.externalRef}</span>}
                  {r.source === 'api' && <span className="api" title={r.clientName}>API</span>}
                  {r.source === 'api' && r.clientName && <span className="dim">{r.clientName}</span>}
                  <span className="dim">· {r.creator}</span>
                  {who && <span className="dim rexec" title={t('rlKExecutor')}>→ {t('rlExecShort', { who })}</span>}
                </div>
              </div>
            )
          })}
          {!releases.length && <div className="empty">{filtering ? t('rlNoMatch') : t('rlEmpty')}</div>}
        </div>

        {/* 右栏:详情 */}
        {open
          ? (
            <ReleaseDetailPane
              open={open} openStage={openStage} codeTab={codeTab} onCodeTab={setCodeTab}
              onToggleStage={toggleStage} onAbort={abort} onContinue={continueStage}
            />
          )
          : <div className="detail card empty">{t('rlPickRelease')}</div>}
      </div>

      {/* create modal */}
      {formOpen && <ReleaseForm conns={conns} pipelines={pipelines} onClose={() => setFormOpen(false)} onCreated={onCreated} />}
    </div>
  )
}
