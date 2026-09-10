// Front/back contract types (mirror backend dto package).

/**
 * An environment code — the group an instance belongs to (`prod`, `prod-hk`, …).
 *
 * This was a union of the four built-in strings. Environments and tiers are rows
 * an administrator creates now, so no compile-time set can be complete; anything
 * that maps a code to a label or colour must therefore handle a code it has
 * never seen (a deleted environment still appears in history) instead of relying
 * on the type to rule it out.
 */
export type Env = string
/** A control-tier code — what the rules are keyed by. Also open-ended. */
export type TierCode = string
export type CapLevel = 'allow' | 'approve' | 'deny'
export type RiskLevel = 'high' | 'mid' | 'off' | 'low'
export type MenuKey = 'terminal' | 'approve' | 'db' | 'rules' | 'envtier' | 'perms' | 'audit' | 'settings'

/**
 * A control tier: the unit the capability matrix and risk dictionary are keyed
 * by. The booleans replace what used to be `code === 'prod'` tests in the UI and
 * the gateway alike.
 */
// 执行窗口(「班车」):在指定时间、对指定的库,把本来要审批的中/高风险语句直接放行。
// 它改的是"要不要人来批",不是"有没有权限" —— 能力矩阵拒绝的仍然拒绝。
export interface ExecWindow {
  id: number
  name: string
  enabled: boolean
  connectionId: number
  database: string
  kind: 'once' | 'recurring'
  timezone: string
  startsAt?: string
  endsAt?: string
  weekdays: string   // ISO 星期的逗号列表,1=周一…7=周日;空串=每天
  startMin: number   // 从当地 00:00 起的分钟数
  endMin: number     // 小于等于 startMin 表示跨午夜
  notAfter?: string
  reason: string
  createdBy: number
  createdAt: string
  updatedAt: string
  /**
   * 审批状态。判定层只认 approved —— 一张还在等审批(或被驳回)的窗口一行都不放行。
   *
   * 它和 enabled / active 是三件事:status 说"有没有人签过字",enabled 说"运维要不要
   * 用它",active 说"此刻在不在时间表内"。三者都为真,这扇门才是开着的。
   */
  status: 'pending' | 'approved' | 'rejected' | 'cancelled'
  approvalId?: number
  apNo?: string
  decidedAt?: string
  // 此刻是否开着 —— 由后端用与判定完全相同的逻辑算出来。前端不自己算:跨午夜与
  // 时区换算写两遍迟早分叉,而分叉的表现是"界面说开着、网关说没开"。
  active: boolean
}

export interface EnvTier {
  code: TierCode
  displayName: string
  sortOrder: number
  requireMfa: boolean
  dangerBanner: boolean
  countsInPending: boolean
  scanBaseline: boolean
  strictNoWhere: boolean
  connLayer: string
  defaultRole: string
}

/** A group of instances, bound to exactly one tier. Many environments per tier. */
export interface Environment {
  code: Env
  displayName: string
  tierCode: TierCode
  sortOrder: number
}

export interface Me {
  id: number
  name: string
  email: string
  initials: string
  roleId: number
  roleCode: string
  roleName: string
  layer: string
  roleIds: number[]
  roleNames: string[]
  roleCodes: string[]
  canApprove: boolean
  mfaEnabled: boolean
  menus: Record<string, boolean>
  capabilities: Record<string, Record<string, string>>
}

export interface LoginResp {
  token: string
  expiresAt: string
  user: Me
}

/**
 * The machine-readable identity of the rule behind a verdict.
 *
 * Sent alongside the canonical Chinese rule string so the UI can say the same
 * thing in the reader's language; `parts` nests (a batch holds one per gated
 * statement, an execution window holds the verdict it relaxed). See
 * lib/ruleText.ts — an unknown `code` falls back to the string.
 */
export interface RuleRef {
  code: string
  args?: Record<string, string>
  parts?: RuleRef[]
}

export interface RiskCheckResp {
  risk: string
  action: 'allow' | 'approve' | 'deny'
  requiresApproval: boolean
  matchedRule: string
  matchedRuleRef?: RuleRef
  command: string
  approvalNo?: string
  auditId?: string
}

export interface ExecResp {
  intercepted: boolean
  approvalNo?: string
  auditId?: string
  risk: string
  rule?: string
  ruleRef?: RuleRef
  output?: string
  rows?: number
  ms?: number
}

export interface Connection {
  id: number
  name: string
  engine: string
  host: string
  port: number
  env: Env
  policy: string
  defaultRole: string
  layer: string
  tags: string
  username?: string
  password?: string
  database?: string
  status: 'online' | 'maint'
}

// 项目:数据库与升级单的归属。组织维度,不是安全边界(访问范围仍由 tags 决定)。
/**
 * 敏感字段规则。命中的列在**服务端**打码后才回传 —— 前端拿到的已经是星号,
 * 不做也不该做任何脱敏工作。
 */
export interface SensitiveColumn {
  id: number
  /** 表名;'*' = 所有表 */
  tableName: string
  columnName: string
  maskStyle: 'partial' | 'full' | 'hash'
  enabled: boolean
  note: string
  createdAt?: string
}

export interface Project {
  id: number
  name: string
  owner: string
  description: string
  /** 名下的数据库数 —— 删除受它约束 */
  databases: number
  /** 名下的升级单数(含库已改挂他处的历史单据) */
  releases: number
  createdAt: string
}

export interface RoleBrief {
  id: number
  code: string
  name: string
  layer: string
  icon: string
  count: number
}

export interface Member {
  /** human | service —— 服务账号不能审批,审批人展示要排除 */
  kind?: string
  id: number
  name: string
  initials: string
  dept: string
}

export interface RoleDetail {
  id: number
  code: string
  name: string
  layer: string
  icon: string
  menus: Record<string, boolean>
  matrix: Record<string, Record<string, string>>
  members: Member[]
  memberIds: number[]
  tags: string[]
}

export interface UserView {
  id: number
  name: string
  email: string
  initials: string
  dept: string
  roles: string[]
  roleIds: number[]
  primaryRoleId: number
  status: 'active' | 'disabled' | 'invited'
  kind?: 'human' | 'service' // 服务账号在列表里要能一眼认出来
  mfaEnabled: boolean
  lastActive: string
  /** 这一行现在能不能被停用 —— 服务端算好带下来,前端不自己判(与 canDecide 同理) */
  canDisable?: boolean
  /** 不能停用时的原因,直接显示给人看;能停用时为空 */
  disableBlock?: string
}

// 服务账号 —— 对接升级单/CI/CD 系统的机器主体(kind=service 的用户)。
export interface ServiceAccount {
  id: number
  name: string
  email: string // 生成的唯一标识,不是邮箱
  status: string
  dept: string
  roles: string[]
  tags: string[]
  clients: number // 已绑定的 API 凭据数
}

export interface RiskCommandView {
  command: string
  /** tier code -> high|mid|off. Rules are keyed by CONTROL TIER: prod-hk owns
   *  no rows of its own and is governed by the prod tier it binds to. */
  tiers: Record<TierCode, string>
}

export interface ApprovalStep {
  id: number
  stepOrder: number
  approverId: number
  approver: string
  status: string
}

export interface Approval {
  /** 当前登录者此刻能不能决定这张单 —— 服务端算好带下来,前端不再自己判一遍 */
  canDecide?: boolean
  /** 不能决定时的理由,显示在按钮原来的位置;可决定时为空 */
  blockReason?: string
  /** 这张单此刻在等我去执行吗 —— 同样由服务端算好(service.CanExecuteApproved),
   *  前端不自己去拼 status / executedAt / 是不是发起人 这三件事 */
  canExecute?: boolean
  /** 执行时刻;为空表示"批了但还没跑"。审批通过不再代执行,这是那一步的分界 */
  executedAt?: string | null
  id: number
  apNo: string
  connectionId: number
  /** Where it ran, and what it was judged under — snapshots, never re-resolved. */
  env: string
  /** Empty on tickets raised before tiers existed; show as unknown, don't infer. */
  tierCode?: string
  instance: string
  database: string
  command: string
  keyword: string
  initiatorId: number
  initiator: string
  reason: string
  riskLevel: string
  /** >0 表示这张单属于一张发布单:它的执行归流水线,走不了发起人手动执行那条路 */
  releaseId?: number
  /** 窗口申请单:批准即生效,没有需要手动执行的命令(与 releaseId 同一类)。 */
  windowId?: number
  /**
   * 能不能**撤回**这张单。与 canDecide 分开:撤回不是"决定"。
   *
   * 驳回是审批人看过之后说"不行",要留在记录里;撤回是发起人说"这张不用了",
   * 没有人对它做过判断。服务端算这一位,前端照着它决定按钮亮不亮。
   */
  canCancel?: boolean
  /**
   * cancelled 是发起人自己收回的,与 rejected 分开 —— 后者是审批人看过之后说
   * "不行"。合成一个的话,记录上就看不出到底有没有人拒绝过什么。
   */
  status: 'pending' | 'approved' | 'rejected' | 'expired' | 'cancelled'
  /** 那一次下发的结果:空 = 还没执行,或是这一列存在之前跑过的历史单(成败无从得知)。
   *  它和 status 是两件事 —— 跑挂了不会把一张已批准的工单变回没批准。 */
  execStatus?: 'success' | 'failed' | ''
  auditId: string
  result: string
  resultRows: number
  decidedAt: string | null
  createdAt: string
  steps: ApprovalStep[]
}

export interface AuditRow {
  id: number
  occurredAt: string
  actor: string
  instance: string
  database: string
  command: string
  risk: string
  result: string
  approvalNo: string
  // Who actually authorised the action when that is not the actor (an external
  // approver, or an administrator acting on another account). Empty = same person.
  operator?: string
  hash: string
}

// AuditPage is one page of audit rows plus the total matching the filters.
export interface AuditPage {
  items: AuditRow[]
  total: number
  page: number
  pageSize: number
}

// AuditQuery captures the audit filters: risk, a relative range OR an absolute
// from/to window, and pagination.
export interface AuditQuery {
  risk: string
  range?: string
  from?: string
  to?: string
  page?: number
  pageSize?: number
}

export interface ScannedStmt {
  index: number
  sql: string
  command: string
  risk: 'high' | 'mid' | 'safe'
  noWhere: boolean
}

export interface ScriptScanResp {
  filename: string
  total: number
  high: number
  mid: number
  safe: number
  hasRisky: boolean
  statements: ScannedStmt[]
}

export interface ExportJob {
  id: number
  connectionId: number
  instance: string
  database: string
  sql: string
  name: string
  /** awaiting = 含敏感字段,批准之前不进队列 */
  status: 'awaiting' | 'pending' | 'running' | 'done' | 'failed' | 'expired'
  includeSensitive?: boolean
  apNo?: string
  rows: number
  bytes: number
  parts: number
  files: string // newline-joined part paths
  password: string
  error: string
  createdAt: string
  finishedAt: string | null
}

// AsyncJob — a long-running SQL run in the background, with streamed progress log.
export interface AsyncJob {
  id: number
  connectionId: number
  instance: string
  database: string
  sql: string
  reason: string
  status: 'pending' | 'running' | 'done' | 'failed' | string
  log: string
  rows: number
  error: string
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
}

export interface ScriptUpload {
  id: number
  userId: number
  filename: string
  path: string
  size: number
  source: 'upload' | 'terminal' | string
  createdAt: string
}

/**
 * A saved terminal script bound to a hotkey. Owned by its author — the API only
 * ever returns the caller's own.
 *
 * There is no risk/approval field on purpose. A snippet is text, not a stored
 * verdict: firing the hotkey submits `body` through the normal exec path, so it
 * is judged against the instance it lands on, when it lands there.
 */
export interface TerminalSnippet {
  id: number
  userId: number
  name: string
  body: string
  slot: number // 1-9 = Alt+N, 0 = saved but unbound
  createdAt: string
  updatedAt: string
}

export interface SnippetLimits {
  maxBytes: number
  maxName: number
  maxSlot: number
  max: number
}

export interface Notification {
  id: number
  userId: number
  type: 'approval-approved' | 'approval-rejected' | 'approval-expired' | string
  title: string
  body: string
  refNo: string
  read: boolean
  createdAt: string
}

export interface WebhookConfig {
  id: number
  endpoint: string
  secret: string
  events: string
  retryMax: number
  enabled: boolean
}

/** 树上的一个库。抽成具名类型,是为了让"往这个库里补表"的函数能接住它。 */
export interface SchemaDB {
  name: string
  /** 归属项目 —— 组织维度,判定层不看。0 / 缺省 = 未归属,是合法状态。 */
  projectId?: number
  projectName?: string
  schemas?: { name: string; tables: { name: string }[] }[]
  tables: { name: string }[]
}

export interface ConnectionSchema {
  connectionId: number
  databases: SchemaDB[]
  error?: string
}

/** Programmable objects under one database/schema/owner, grouped by kind. */
export interface DbObjects {
  functions: string[]
  procedures: string[]
  packages: string[]
  triggers: string[]
  error?: string
}

/**
 * 一个 INVALID 的 Oracle 可编译对象。
 *
 * units 是具体哪几个单元失效了:包的规范是好的、只有包体 INVALID,和两者都 INVALID,
 * 对 DBA 不是一回事。编译动作按 kind 走(编包 = 规范加包体一起编)。
 */
export interface InvalidObject {
  owner: string
  name: string
  kind: string
  units: string[]
}

/** 一个对象重编译之后的结局。status 是回读 all_objects 得到的,不是"调用成功了"。 */
export interface RecompileItem {
  owner: string
  name: string
  kind: string
  status: 'VALID' | 'INVALID' | 'ERROR'
  errors?: { type: string; line: number; position: number; text: string }[]
  err?: string
  passes: number
}

/** 一次批量重编译的结果。skipped 是超出单次上限、这次没碰的。 */
export interface RecompileReport {
  owner: string
  total: number
  fixed: number
  failed: number
  skipped: number
  passes: number
  ms: number
  items: RecompileItem[]
}

/** One programmable object's source text. */
export interface ObjectSource {
  name: string
  type: string
  source: string
}

export interface WebhookDelivery {
  id: number
  event: string
  endpoint: string
  success: boolean
  status: string
  attempts: number
  createdAt: string
}

// ---------------------------------------------------------------- 规范审查

/** A review dialect. Open-ended for the same reason Env is: the server owns the
 *  list, and a rule may name a dialect this build has never heard of. */
export type ReviewDialect = string
export type ReviewLevel = 'error' | 'warn' | 'info'

/** One rule in the 规范审查规则库. `kind` decides what an operator may change:
 *  a builtin rule's code/scope binds it to a checker, a regex rule is theirs. */
/**
 * 规范分级 —— 公司四份规范(MySQL / TiDB / Oracle / Huawei DWS)共用的三级词汇。
 * 空串 = 规范未覆盖,平台内置的防护。它和 level 是两件事:level 是这条发现在本
 * 平台值多少钱,spec 是规范怎么定性这条要求 —— 把某条降成告警不等于改了规范。
 */
export type ReviewSpec = 'critical' | 'mandatory' | 'recommended' | ''

export interface ReviewRule {
  id: number
  code: string
  name: string
  dialect: ReviewDialect
  category: string
  level: ReviewLevel
  kind: 'builtin' | 'regex'
  enabled: boolean
  params: string
  message: string
  sortOrder: number
  /** 规范分级;空 = 平台内置 */
  spec: ReviewSpec
  /** 出处,如 "Huawei DWS 規範 §5.3 分布鍵";空 = 平台内置 */
  specRef: string
}

export interface ReviewFinding {
  code: string
  name: string
  level: ReviewLevel
  category: string
  stmt: number
  line: number
  sql: string
  message: string
}

export interface ReviewResult {
  dialect: ReviewDialect
  statements: number
  errors: number
  warnings: number
  infos: number
  findings: ReviewFinding[]
  /** No ERROR-level finding — not "no findings". Warnings never block. */
  passed: boolean
}

export interface ReviewCheckResp {
  instance: string
  result: ReviewResult
}

export interface ReviewCatalog {
  dialects: string[]
  categories: string[]
  levels: string[]
  /** 规范分级的展示顺序 */
  specs?: string[]
}

// ---------------------------------------------------------------- 发布流水线

export type StageType = 'review' | 'approve' | 'backup' | 'execute' | 'verify' | 'manual' | 'notify'
export type RunStatus = 'pending' | 'running' | 'waiting' | 'success' | 'failed' | 'skipped' | 'aborted'

export interface PipelineStage {
  id?: number
  pipelineId?: number
  stepOrder?: number
  name: string
  type: StageType
  config: string
  onFailure: 'abort' | 'continue'
}

export interface Pipeline {
  id: number
  name: string
  description: string
  /** Empty = every control tier; a value narrows the flow to that tier. */
  tierCode: string
  enabled: boolean
  isDefault: boolean
  createdBy?: number
  createdAt?: string
  updatedAt?: string
  stages: PipelineStage[]
}

/** One stage of one run — what the pipeline view draws. */
export interface ReleaseStage {
  id: number
  releaseId: number
  stepOrder: number
  name: string
  type: StageType
  config: string
  onFailure: string
  status: RunStatus
  log: string
  /** JSON-encoded ReviewResult on a review stage; empty otherwise. */
  findings: string
  approvalId: number
  approvalNo: string
  rows: number
  startedAt: string | null
  finishedAt: string | null
}

export interface Release {
  id: number
  relNo: string
  title: string
  pipelineId: number
  pipelineName: string
  connectionId: number
  instance: string
  database: string
  /** Snapshots taken when the run started; never re-resolved. */
  env: string
  tierCode: string
  engine: string
  changeType?: 'dml' | 'ddl' | '' // 变更类型;历史单为空
  /** 归属项目,提交时从目标库快照 —— 库以后改挂别处,历史单据不改账 */
  projectId?: number
  projectName?: string
  sql: string
  scriptUploadId?: number
  reason: string
  creatorId: number
  creator: string
  /** Which door the ticket came in by. An API release also carries the external
   *  system's name and its own ticket id, so the two systems can talk about the
   *  same change. */
  source: 'console' | 'api'
  clientName?: string
  externalRef?: string
  status: RunStatus
  risk: string
  error: string
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
  stages: ReleaseStage[]
}

// ---------------------------------------------------------------- 开放接口

/** One external system's credential. The secret is NOT here and never will be —
 *  the server stores a bcrypt hash and returns the plaintext once, at creation. */
export interface APIClient {
  id: number
  name: string
  /** Public half of the credential, safe to display. */
  key: string
  userId: number
  /** The service account this client acts as — its roles decide what the
   *  credential may release, and the audit trail records it as the actor. */
  userName: string
  allowIps: string
  scopes: string
  /** 该凭据建单固定走的发布流程;0 = 未绑定,走目标分层的默认流程。外部请求不可指定流程。 */
  pipelineId: number
  enabled: boolean
  lastUsedAt: string | null
  createdAt: string
}

export interface ReleasePage {
  items: Release[]
  total: number
  page: number
  pageSize: number
}

export interface SettingsResp {
  settings: Record<string, string>
  webhook: WebhookConfig | null
  // Secret values are never returned; these flags say whether one is on file so
  // the UI can show "已配置" and treat a blank input as "keep unchanged".
  secretsSet?: Record<string, boolean>
  webhookHasSecret?: boolean
}
