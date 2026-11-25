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
  /** 第一个库(老数据只有它) */
  database: string
  /** 窗口勾选的全部库(同一台实例上可多选,2026-10-22) */
  databases?: string[]
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
  /** 申请人姓名:只在管理后台的全局列表里有(scope=all) */
  createdByName?: string
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
  // 这扇门是不是再也不会开了。同样由后端算,理由同上。
  //
  // 没有它,界面只能分出"开着"和"没开着",于是一个上周就结束的一次性窗口会一直显示成
  // "未到点" —— 一句读起来像"再等等就到了"的话,而它永远不会到。周期班车只有设了停运
  // 时刻并且已过才算到期。它只描述时间表,与审批状态、启用与否无关。
  expired: boolean
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
  maskSensitive: boolean
  bizExportRestrict: boolean
  /** 这一层上的发布流程能不能用「自动执行」阶段(不等人点「确认执行」)。默认只有 dev / uat 开。 */
  autoExecute: boolean
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
  /** 发布页能否切到「全部」(后端 canSeeAllReleases:admin / owner / l2 / audit) */
  canSeeAllReleases?: boolean
  mfaEnabled: boolean
  menus: Record<string, boolean>
  capabilities: Record<string, Record<string, string>>
}

// SRD 授权登录(公司 SSO):登录页据此决定要不要露出入口、本地表单要不要收起。
// loginUrl 是 {SRD}/auth/login,前端拼上 ?redirect=<网关登录页> 再跳。
export interface SsoConfig {
  enabled: boolean
  loginUrl: string
  /** 部署配置(sso.disabled / AEGISDB_SSO_DISABLED)强制关闭了 SSO:设置页的开关暂不生效 */
  forcedOff?: boolean
}

export interface LoginResp {
  session: string // 会话标记,不是凭据(lib/session.ts)
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
  /** 生产大表直接 DDL,可能长时间锁表 —— 提交之前先给人看见 */
  notice?: string
  noticeRef?: RuleRef
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
  connMs?: number
  queryMs?: number
  fetchMs?: number
  /** 行读完之后网关自己的处理(敏感字段打码) */
  processMs?: number
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
  /** 业务查询源:只有它能被查询授权引用(约定指向从库、只读库账号) */
  bizQuery?: boolean
  username?: string
  password?: string
  database?: string
  /** 从库(只读副本):配了之后业务方的数据查询都由它提供。端口 0 / 账号空 = 沿用主库 */
  replicaHost?: string
  replicaPort?: number
  replicaUsername?: string
  /** 只写:保存从库密码用,服务端不回传;留空 = 保持不变 */
  replicaPassword?: string
  status: 'online' | 'maint'
  /**
   * 上一次连通性巡检的结论 —— 和 status 是两件事。
   *
   * status 是**人为标的维护态**,永远不表示"连得上"。把两者混为一谈,就是"仅看异常"
   * 永远筛不出连不上的实例的原因。
   *
   * lastCheckOk 缺省(undefined)表示**从没探过**。它和"探过、通了"是两回事:一个从没
   * 被探过的实例显示成"正常",是在替它担保。
   */
  lastCheckAt?: string
  lastCheckOk?: boolean
  /** 失败原文。只有管理员拿得到 —— 驱动原始错误里带网关出口 IP、pg_hba 拒绝详情。 */
  lastCheckErr?: string
  /** 结论种类:ok | unreachable | auth。定时巡检据它跳过"上次认证失败"的实例。 */
  lastCheckKind?: 'ok' | 'unreachable' | 'auth'
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
  noInstances?: boolean // 新角色还没选标签:不开放任何实例(不是"未设置标签 = 全部")
  id: number
  code: string
  name: string
  layer: string
  icon: string
  description?: string
  defaultConnRole?: string
  canApprove?: boolean
  /** 自定义角色(经「新建角色」建的)才能删 */
  deletable?: boolean
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
  /** 已绑定公司 SSO:只能经 SSO 登录(管理员除外) */
  ssoBound?: boolean
  ssoUserName?: string
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
  /** blockReason 的机器可读身份(apiMsg),界面按读者语言显示 */
  blockReasonRef?: RuleRef
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
  /** 描述类单子(执行窗口)的机器可读描述;按读者语言显示,没有就显示 command(lib/ruleText renderApText) */
  commandRef?: RuleRef
  keyword: string
  initiatorId: number
  initiator: string
  reason: string
  riskLevel: string
  /** >0 表示这张单属于一张发布单:它的执行归流水线,走不了发起人手动执行那条路 */
  releaseId?: number
  /** 窗口申请单:批准即生效,没有需要手动执行的命令(与 releaseId 同一类)。 */
  windowId?: number
  /** 导出申请单:批准后由后台导出任务执行。 */
  exportJobId?: number
  /** 脚本工单:command 只是摘要,全文经 api.approvalScript 按需拉取。 */
  scriptUploadId?: number
  /**
   * 工单类型,**服务端推好带下来**(model.ApprovalType):三个外键都为 0 = 命令单。
   * 前端不照着 releaseId / windowId / exportJobId 再推一遍 —— 筛选的 WHERE 在服务端,
   * 标签若按另一份推断来画,迟早会出现"筛出来的行挂着别的标签"。
   */
  type?: 'cmd' | 'release' | 'window' | 'export'
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
  execStatus?: 'success' | 'failed' | 'running' | ''
  /** 在线变更工单(生产大表加 / 删索引)挂的任务 */
  oscJobId?: number
  /** 要审批人特别留意的一句话(生产大表直接 DDL,可能长时间锁表) */
  notice?: string
  noticeRef?: RuleRef
  auditId: string
  result: string
  /** result 的机器可读身份(JSON 串,apiMsg / outText;迁移 0060) */
  resultRef?: string
  resultRows: number
  decidedAt: string | null
  /** 决定来自哪个渠道(console | api | lark | telegram);空 = 没有人做过决定,或这一列之前的老单 */
  decidedVia?: string
  /** 拒绝时填的原因(只有聊天卡片上能填);没填为空 */
  rejectReason?: string
  createdAt: string
  steps: ApprovalStep[]
}

/** 聊天审批机器人的渠道。后端另有 telegram,界面上还没有它。 */
export type ChatChannel = 'lark'

/** 本人在一个开着的渠道上的绑定状态。 */
export interface ChatBinding {
  channel: string
  botName: string
  bound: boolean
  boundAt?: string
}

/** 一次性绑定码:只显示这一次。 */
export interface ChatBindCode {
  code: string
  expiresAt: string
  botName: string
}

/** 一个渠道的连接状态(管理员看)。 */
export interface ChatStatus {
  enabled: boolean
  state: 'off' | 'connecting' | 'connected' | 'conflict' | 'error'
  /** 冲突 / 出错的原因;detailRef 非空时按读者语言显示 */
  detail: string
  detailRef?: RuleRef
  botName: string
}

export interface AuditRow {
  id: number
  occurredAt: string
  actor: string
  instance: string
  /**
   * 判定当时的环境与分层快照 —— 与审批单上的那一对同源、同理由(见 Approval.tierCode):
   * 这一行说它被判成了 high,只有当时在管的那个分层解释得了为什么。两者都盖在哈希链下。
   * 早于分层模型的行为空;界面按分层给实例标签着色,空就退回中性色,不去猜。
   */
  env?: string
  tierCode?: string
  database: string
  command: string
  risk: string
  result: string
  approvalNo: string
  // Who actually authorised the action when that is not the actor (an external
  // approver, or an administrator acting on another account). Empty = same person.
  operator?: string
  /**
   * 这一行是哪一类事件:被拦下来 / 审批决定 / 真执行了 / 登录。
   *
   * 空 = 迁移 0039 之前写下的行,当时还没有这一列。**不回填**,所以界面要如实显示"—",
   * 而不是替它猜一个 —— 在一张哈希链审计表上猜等于造证据。
   */
  eventType?: 'intercept' | 'approve' | 'exec' | 'login' | ''
  /**
   * 这条命令对**数据**做了什么:增 / 删 / 改 / 查,或者 other(它根本不是对数据的操作 ——
   * 登录、配置变更、流程标签,也可能是认不出的语句)。
   *
   * 与 eventType 正交。空 = 迁移 0040 之前写下的行;空与 other 不是一回事,后者是一个
   * 真实的结论。
   */
  dataAction?: 'create' | 'delete' | 'update' | 'read' | 'other' | ''
  /**
   * 这一下是从**哪台机器**发出来的(迁移 0041)。
   *
   * actor 回答"谁",这一列回答"人在哪" —— 同一个账号从两个网段各删过一次数据,只看
   * actor 是一样的两行。空 = 0041 之前写下的行,或者那一下根本不是从 HTTP 请求来的
   * (定时清扫、启动期回填这类后台动作本来就没有客户端)。同样不回填。
   */
  clientIp?: string
  /**
   * 这一下是从网关的**哪个入口**进来的(迁移 0063):数据查询 / Web 命令行 / 上传脚本 /
   * 数据导出 / 审批单 / 变更发布 / 开放接口 / 登录认证 / 管理操作 / 系统任务。
   * 空 = 0063 之前写下的旧行,显示「未知 / 旧记录」,不回填。进哈希链。
   */
  source?: AuditSource | ''
  hash: string
}

/** 审计行的入口来源,与后端 model.AuditSources 同一组取值 */
export type AuditSource =
  | 'query' | 'terminal' | 'script' | 'export' | 'approval'
  | 'release' | 'api' | 'auth' | 'admin' | 'system'

// AuditPage is one page of audit rows plus the total matching the filters.
/**
 * 窗口期里下发过的一条语句 —— 回看一扇门"那几个小时里到底跑了什么"时的一行。
 *
 * byWindow 是这里唯一需要小心读的字段:它说这一条是**被这扇门放行的**(本来要审批),
 * 判据是执行当时写进审计哈希链的 Operator,不是按时间反推的。其余的行只是恰好在窗口期
 * 里跑的 —— 低风险语句本来就不用审批,窗口开不开都一样。
 */
export interface WindowStatement {
  auditId: number
  occurredAt: string
  actor: string
  command: string
  risk: string
  result: string
  dataAction?: string
  approvalNo?: string
  byWindow: boolean
}

export interface WindowStatements {
  windowId: number
  items: WindowStatement[]
  /** 其中被这扇门放行的条数 —— 这一页真正要回答的那个数。 */
  byWindow: number
  /** 扫描到了上限,更早的窗口期没看。界面必须说出来:被截断的列表看起来和"就这么多"一样。 */
  truncated: boolean
}

export interface AuditPage {
  items: AuditRow[]
  total: number
  page: number
  pageSize: number
  /** 按审批单合并时:本页每张审批单共几条操作(审批单号 → 条数) */
  groups?: Record<string, number>
}

/**
 * 一次哈希链校验的结论。
 *
 * 只报**第一处**对不上的行:链是前后咬合的,第一处断掉之后它后面的每一行都会跟着对不上,
 * 数出来的"坏了几行"没有意义,只会让人以为损坏面很大。
 */
export interface AuditChainReport {
  /** 这一趟实际验过多少行(界面只验最近一段,所以可能小于 total) */
  checked: number
  total: number
  ok: boolean
  fromId: number
  toId: number
  /** ok 为 true 时以下都是零值 */
  badId: number
  badAt: string
  badActor: string
  /** content = 内容被改过 · link = 链断了(有行被删/插) · gen = 被降代重算过 */
  badReason: '' | 'content' | 'link' | 'gen'
  /** 最后验过的那一行用的是第几代键集 —— 随迁移往上走,不是错误 */
  gen: number
  /** 按毫秒进位放行的旧行数(库里时间比哈希晚整 1 秒,修复前写入的),0 表示没有 */
  rolloverRows: number
}

// AuditQuery captures the audit filters: risk, a relative range OR an absolute
// from/to window, and pagination.
export interface AuditQuery {
  risk: string
  /**
   * 事件类型筛选;空数组 = 不筛。**多选**,发给服务端时逗号分隔。
   * 和 risk 一样在服务端筛 —— 列表是分页的,只筛当前页会对一条躺在第三页的记录回答"没有"。
   */
  event?: string[]
  /** 增删改查筛选;空数组 = 不筛。与 event 正交,同时给就是取交集 */
  action?: string[]
  /** 入口来源筛选;空数组 = 不筛。'unknown' 筛 0063 之前没有来源的旧行 */
  source?: string[]
  /** 实例名精确筛;可选值来自 api.auditInstances(审计里真实出现过的那些) */
  instance?: string
  /** 模糊匹配操作人**或**来源 IP —— 这两样是"是谁、从哪台机器"的两半 */
  q?: string
  range?: string
  from?: string
  to?: string
  /** 「我的操作记录」:只看自己的(能看全部的管理员 / 审计员也一样) */
  mine?: boolean
  /** 管理后台取全量:管理后台 IP 白名单 / 空闲超时不过就报 40303 / 40304,而不是降级成只看自己的 */
  full?: boolean
  /** 'approval' = 同一张审批单的操作合成一条 */
  group?: string
  page?: number
  pageSize?: number
}

export interface ScannedStmt {
  index: number
  sql: string
  command: string
  risk: 'high' | 'mid' | 'safe'
  /** 对当前用户、目标实例的完整判定(能力矩阵 + 字典 + 无 WHERE + 执行窗口) */
  action?: 'allow' | 'approve' | 'deny'
  rule?: string
  noWhere: boolean
}

/** 脚本扫描 / 执行的进度(GET /scripts/progress/:id;上传脚本弹窗轮询) */
export interface ScriptProgress {
  /** scan 字典扫描 / judge 逐条判定 / run 逐条执行 */
  phase: 'scan' | 'judge' | 'run' | ''
  done: number
  total: number
  /** 正在处理的那条语句的开头 */
  current?: string
  startedAt: string
  finished: boolean
}

export interface ScriptScanResp {
  filename: string
  total: number
  high: number
  mid: number
  safe: number
  hasRisky: boolean
  /** 有语句被禁止:整份脚本不执行、也不送审 */
  blocked?: boolean
  blockedRule?: string
  /** blockedRule 的机器可读身份(apiMsg) */
  blockedRuleRef?: RuleRef
  /** 本该审批的语句由执行窗口(班车)放行时,是哪个窗口;此时 hasRisky 为 false */
  window?: string
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
  status: 'awaiting' | 'pending' | 'running' | 'done' | 'failed' | 'expired' | 'cancelled'
  includeSensitive?: boolean
  /** csv(默认)| sql —— INSERT 语句,单表查询附建表语句 */
  format?: 'csv' | 'sql'
  targetTable?: string
  apNo?: string
  rows: number
  bytes: number
  parts: number
  files: string // newline-joined part paths
  password: string
  error: string
  /** error 的机器可读身份(JSON 串,apiMsg;迁移 0060) */
  errorRef?: string
  createdAt: string
  finishedAt: string | null
  downloadedAt?: string | null // 最近一次下载(迁移 0057)
  downloadedFiles?: string // 下过的分卷路径,换行分隔
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
  /** 日志里网关自己那几行的身份(JSON 的 [{t, r}];迁移 0061) */
  logRefs?: string
  rows: number
  error: string
  /** error 的机器可读身份(JSON 串,apiMsg;迁移 0060) */
  errorRef?: string
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
}

export interface ScriptUpload {
  id: number
  userId: number
  filename: string
  /** 服务器上存的文件名(相对本人的上传目录);后端不回绝对路径 */
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
  type: 'approval-approved' | 'approval-rejected' | 'approval-expired' | 'exec-failed' | string
  title: string
  body: string
  /** 标题 / 正文的机器可读身份(JSON 串,apiMsg;迁移 0060) */
  titleRef?: string
  bodyRef?: string
  refNo: string
  read: boolean
  createdAt: string
}

/** 管理后台「在线用户」的一行:一个登录会话(见 GET /online-users) */
export interface OnlineSession {
  userId: number
  name: string
  email: string
  dept: string
  initials: string
  ip: string
  browser: string
  loginAt?: string
  lastSeen: string
  lastAction?: string
  terminals: { connectionId?: number; instance: string; openedAt: string; lastExecAt?: string; running: boolean }[]
}

/** 在线变更任务(GET /osc/jobs/:id) */
export interface OSCJobView {
  id: number
  connectionId: number
  instance: string
  database: string
  table: string
  statement: string
  apNo?: string
  releaseId?: number
  initiator: string
  sizeRows: number
  sizeBytes: number
  /** 前置检查警告,JSON 数组 [{code, reason}] */
  warnings?: string
  status: 'awaiting' | 'running' | 'done' | 'failed' | 'aborted' | 'interrupted'
  phase: string
  rowsEstimated: number
  rowsCopied: number
  chunks: number
  rowsReplayed: number
  cutOverAttempts: number
  throttled: boolean
  /** 为什么在限流(引擎的规范中文):主库负载 / 从库延迟 / 从库延迟未知 */
  throttleReason?: string
  /** throttleReason 的机器可读身份(JSON 串,apiMsg;迁移 0061) */
  throttleReasonRef?: string
  paused: boolean
  error?: string
  errorRef?: string
  oldTable?: string
  startedAt?: string
  finishedAt?: string
  canControl: boolean
}

export interface WebhookConfig {
  id: number
  endpoint: string
  secret: string
  events: string
  /** 推送的环境代码(逗号分隔),空 = 全部环境 */
  envs: string
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
/**
 * 一台实例最近一次元数据同步的结果。
 *
 * 它不是"可有可无的进度条":没有它,"这台实例为什么一张表都没有"就没有答案 ——
 * 是还没轮到它、连不上、账号没权限,还是它真的空着?界面必须能把这几种分开说。
 *
 * err 非空表示上一次同步失败,此时那几个计数是**上一次成功**留下的旧值。
 */
/**
 * 收单那一刻的语法与对象检查结果。
 *
 * objectSource 是读这份结果时最要紧的一栏:cache 说明对象结论来自本地缓存(可能过期,
 * cacheSyncedAt 是它的时间),live 是当场查的目标库,none 表示根本没能核对 —— 而
 * "没能核对"和"核对过、都在"是两回事。
 *
 * syntaxSkipped 同理:DDL 无法在不执行的前提下校验语法,所以"没报语法错"不等于
 * "语法都对"。两个数一起看才知道这份结果覆盖了多少。
 */
export interface Precheck {
  statements: number
  errors: number
  warnings: number
  infos: number
  syntaxChecked: number
  syntaxSkipped: number
  objectSource: 'cache' | 'live' | 'none'
  cacheSyncedAt?: string
  note?: string
  /** note 的机器可读形式,一条附注一项;旧单没有 */
  noteRefs?: RuleRef[]
  issues: PrecheckIssue[]
}

export interface PrecheckIssue {
  stmt: number
  level: 'error' | 'warn' | 'info'
  /** syntax | object | skipped —— skipped 是"没做的检查",不是"通过" */
  kind: string
  object?: string
  message: string
  /** message 的机器可读形式(lib/ruleText renderPreText);旧单没有,回落到 message */
  ref?: RuleRef
  /** 对象结论的来源:cache | live */
  source?: string
}

export interface MetaSync {
  connectionId: number
  startedAt: string
  finishedAt: string
  databases: number
  tables: number
  columns: number
  err?: string
}

/** 后台同步的轮询结果。failed 非空 = 这次什么都没收到;为空时看 sync(sync.err = 收下了但不完整)。 */
export interface MetaSyncStatus {
  running: boolean
  failed?: string
  sync: MetaSync | null
}

/** 缓存下来的一张表(或视图)。syncedAt 是这一行的年龄 —— 界面必须显示它。 */
export interface MetaTable {
  id: number
  connectionId: number
  dbName: string
  schemaName: string
  tableName: string
  kind: string
  comment: string
  syncedAt: string
}

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
  /** 依赖层:1 = 不依赖这批里的任何对象;高层依赖低层,低层先编 */
  level: number
  /** 和这批里的别的对象互相依赖,分不出层,靠多轮兜底 */
  cyclic?: boolean
}

/** 一次批量重编译的结果。skipped 是超出单次上限、这次没碰的。 */
export interface RecompileReport {
  owner: string
  total: number
  fixed: number
  failed: number
  skipped: number
  passes: number
  /** 依赖分了几层(utlrp 的做法:被依赖的先编) */
  levels: number
  /** 处于依赖环里、分不出层的对象数 */
  cyclic: number
  /** 依赖图读不到时的说明:这次没有分层,只靠多轮 */
  depsNote?: string
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
  /** message 的机器可读身份(apiMsg),界面按读者语言显示;老的审查结果没有 */
  messageRef?: RuleRef
}

export interface ReviewResult {
  dialect: ReviewDialect
  statements: number
  errors: number
  warnings: number
  infos: number
  /** 示例:每条规则最多 5 条(脚本里最前面的)。命中总数看 rules / total。 */
  findings: ReviewFinding[]
  /** 按规则汇总,错误在前。2026-11 之前存下的老结果没有,界面从 findings 自己算。 */
  rules?: ReviewRuleHit[]
  /** 全部命中条数,可能远大于 findings.length。老结果没有。 */
  total?: number
  /** No ERROR-level finding — not "no findings". Warnings never block. */
  passed: boolean
}

export interface ReviewRuleHit {
  code: string
  name: string
  level: ReviewLevel
  category: string
  /** 这条规则命中的语句总数 */
  count: number
  message: string
  messageRef?: RuleRef
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
  /** 规则编码 → 参数说明;不在其中的内置规则没有可调参数。自定义正则规则在 '_regex' 下。 */
  params?: Record<string, ReviewParamSpec[]>
}

export type ReviewParamType = 'int' | 'text' | 'regex' | 'list' | 'bool' | 'enum'
export interface ReviewParamSpec {
  key: string
  type: ReviewParamType
  /** 留空时生效的值 */
  default: unknown
  options?: string[]
  /** 某些库上默认值不同(如 Oracle 标识符上限 30) */
  dialectDefaults?: Record<string, unknown>
}

// ---------------------------------------------------------------- 发布流水线

export type StageType = 'review' | 'approve' | 'backup' | 'execute' | 'auto_execute' | 'verify' | 'manual' | 'notify'
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
  /** log 的逐行 RuleRef(JSON,rlLog.),界面按语言渲染;老单没有。见 lib/relLogText.ts。 */
  logRefs?: string
  /** JSON-encoded ReviewResult on a review stage; empty otherwise. */
  findings: string
  /** 执行阶段点「确认执行」的人;空 = 未确认,或自动执行阶段未经人手。 */
  confirmedBy?: string
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
  /** error 的 RuleRef(JSON,rlLog.);老单没有。 */
  errorRef?: string
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
  stages: ReleaseStage[]
  /** 收单检查结果(JSON 文本)。空 = 老单子或这次一条都没检查成,都不是「通过」 */
  precheck?: string
}

/** 发布单 / 阶段的运行状态(与 RunStatus 同一套值,发布页按这个名字读)。 */
export type ReleaseStatus = RunStatus
/** 详情接口返回的发布单:比列表行多带阶段日志、审查结果与收单检查(列表接口不带,见后端 ListReleases)。 */
export type ReleaseDetail = Release
/** 环境胶囊的语义色,由分层决定(envtier.dotFor):prod=danger、staging=warning、dev=success、uat/gli 及自定义=info、查不到=muted。 */
export type EnvType = 'danger' | 'warning' | 'success' | 'info' | 'muted'
/** 流水线步骤条上的一个节点:阶段本身 + 已经算好的展示信息。 */
export interface StepItem {
  stage: ReleaseStage
  label: string
  status: ReleaseStatus
  /** 已用时长(运行中的按现在算);没开始为空串 */
  duration: string
  /** 与前一节点之间连线的状态:done=已流过、live=正流入、''=还没到 */
  conn: '' | 'done' | 'live'
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
  envs?: string[]
}

export interface SettingsResp {
  settings: Record<string, string>
  webhook: WebhookConfig | null
  // Secret values are never returned; these flags say whether one is on file so
  // the UI can show "已配置" and treat a blank input as "keep unchanged".
  secretsSet?: Record<string, boolean>
  webhookHasSecret?: boolean
  /**
   * 能订阅哪些 —— 就是审计行上**「增删改查」那一列**的取值(gateway.DataActions)。
   *
   * 按这一维而不是按事件类型:接 SIEM 的人想说的通常是"所有删除都推给我",而被拦下来的
   * DROP 和执行掉的 DROP 是两个事件类型、同一件事。
   *
   * 由服务端下发,界面不自己列一份:同一组字符串各写一遍的话,只要有一处对不上,表现都是
   * 静默的(勾得到却永远不推,或者推得出却勾不上)。
   */
  webhookActions?: string[]
}

/** 右上角"待办"的一行:此刻在等**当前登录者**动手的一张工单(GET /approvals/todo)。 */
export interface ApprovalTodoItem {
  id: number
  apNo: string
  /** decide = 等我审批;execute = 批过了、等我执行;confirm = 发布单停在人工闸等我放行 */
  kind: 'decide' | 'execute' | 'confirm'
  initiator: string
  /**
   * 发起人是**谁**,不是显示用的那个名字。
   *
   * 登录提示音只对发起人响,而"是不是我提的"按姓名比会在同名的两个人身上悄悄比错 ——
   * 那种错不报任何东西,只是某个人莫名其妙地听不到、或者莫名其妙地听到。
   */
  initiatorId?: number
  instance: string
  database: string
  riskLevel: string
  /** 脱敏且截短的命令预览;完整命令在工单详情里 */
  command: string
  commandRef?: RuleRef
  /** 只有 confirm 那一类有:停在哪一道闸上(manual / execute)。送的是类型,界面自己译 */
  stage?: string
  createdAt: string
}
/** 两个数由服务端逐张按 DecideBlockFor / CanExecuteApproved 判过 —— 只有此刻能动的才算。 */
export interface ApprovalTodo { decide: number; execute: number; confirm: number; items: ApprovalTodoItem[] }

// 查询授权(业务方数据查询)。status 是生效状态。
export interface QueryGrant {
  id: number
  email: string
  userName: string
  dept: string
  sourceId: number
  source: string
  database: string
  maxRows: number
  timeoutSec: number
  effectiveMaxRows: number
  effectiveTimeoutSec: number
  allowExport: boolean
  canExport?: boolean
  startsAt: string
  endsAt: string
  status: 'active' | 'pending' | 'expired' | 'revoked'
  externalId: string
  client: string
  approver: string
  reason: string
  revokedAt?: string
  revokedBy?: string
  updatedAt?: string
}

export interface BizQueryResult {
  columns: string[]
  rows: string[][]
  truncated: boolean
  maxRows: number
  timeoutSec: number
  ms: number
  timedOut: boolean
  maskedColumns?: string[]
  simulated?: boolean
  error?: string
  errorRef?: RuleRef // error 的 code 版本,按读者语言渲染
  /** 由谁提供:replica = 从库;primaryFallback = 从库连不上,回落主库;空 = 数据源没配从库 */
  served?: '' | 'replica' | 'primaryFallback' | 'readonlyAccount'
  replicaError?: string
}

// 业务方数据查询的全局值(设置 · 数据查询)
export interface BizQueryLimits {
  defaultMaxRows: number
  maxRowsCap: number
  defaultTimeoutSec: number
  timeoutCapSec: number
  maxGrantDays: number
  maxConcurrentPerUser: number
  maxConcurrentPerSource: number
  maxDailyPerUser?: number // 0 = 不限
  exportEnabled: boolean
  /** 申请数据查询权限的页面;空 = 不给链接 */
  applyUrl?: string
}

// AI 巡检报告(.scratch/inspection-report)
export interface InspectionFinding { severity: 'critical' | 'warning' | 'info'; category?: string; title: string; object?: string; detail?: string }
export interface InspectionReport {
  id: number; connectionId: number; instanceName: string; reportDate: string; inspectedAt?: string
  score?: number | null; level: 'ok' | 'warning' | 'critical'
  criticalCount: number; warningCount: number; infoCount: number; sizeBytes: number; updatedAt: string
  /** 只在单份报告详情里有 */
  findings?: InspectionFinding[]
  /** 只在最新列表里有:最严重那条问题的标题 */
  topFinding?: string
}
/** 实例列表的一行:实例 + 它最近一份报告(从没收到过就是 null) */
export interface InspectionLatestRow { connectionId: number; instance: string; env: string; engine: string; report: InspectionReport | null }

// 跨环境表结构对比(.scratch/schema-diff)
export interface SchemaDiffConn { id: number; name: string; env: string; engine: string; family: string; database: string }
export interface SchemaDiffOptions { ignoreColumnOrder: boolean; ignoreComments: boolean; ignoreIndexNames: boolean }
export interface SchemaDiffPair { left: string; right: string }
export interface SchemaDiffColumn { name: string; dataType: string; nullable: boolean; hasDefault: boolean; default: string; comment: string; ordinal: number }
export interface SchemaDiffIndex { name: string; columns: string[]; unique: boolean; systemNamed?: boolean; primary?: boolean }
/** 一张表的完整定义(结构不同的表两边各一份,供左右对照) */
export interface SchemaDiffTableDef { name: string; kind: string; comment: string; columns: SchemaDiffColumn[]; primaryKey: string[]; indexes: SchemaDiffIndex[] }
export interface SchemaDiffTable {
  table: string
  kind?: SchemaDiffPair
  comment?: SchemaDiffPair
  primaryKey?: SchemaDiffPair
  columns?: { column: string; status: 'onlyLeft' | 'onlyRight' | 'changed'; left?: string; right?: string; changes?: { attr: string; left: string; right: string }[] }[]
  indexes?: { index: string; status: 'onlyLeft' | 'onlyRight' | 'changed'; left?: string; right?: string }[]
  leftDef?: SchemaDiffTableDef
  rightDef?: SchemaDiffTableDef
}
export interface SchemaDiffSide { connectionId: number; instance: string; env: string; engine: string; database: string; schema?: string; readAt: string; tables: number }
export interface SchemaDiffResp {
  left: SchemaDiffSide
  right: SchemaDiffSide
  diff?: {
    summary: { left: number; right: number; same: number; onlyLeft: number; onlyRight: number; changed: number }
    same: string[]
    onlyLeft: string[]
    onlyRight: string[]
    changed: SchemaDiffTable[]
  }
  error?: string
  errorSide?: 'left' | 'right'
  /** error 的机器可读形式:code = sdReadFailed / sdReadTimeout / sdTooLarge */
  errorRef?: RuleRef
  /** 这一边一张表都没读到(不是错误,多半是库名写错了) */
  emptySide?: 'left' | 'right' | 'both'
}
