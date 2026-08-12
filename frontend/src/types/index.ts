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
export interface EnvTier {
  code: TierCode
  displayName: string
  sortOrder: number
  requireMfa: boolean
  dangerBanner: boolean
  countsInPending: boolean
  scanBaseline: boolean
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

export interface RiskCheckResp {
  risk: string
  action: 'allow' | 'approve' | 'deny'
  requiresApproval: boolean
  matchedRule: string
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

export interface RoleBrief {
  id: number
  code: string
  name: string
  layer: string
  icon: string
  count: number
}

export interface Member {
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
  mfaEnabled: boolean
  lastActive: string
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
  status: 'pending' | 'approved' | 'rejected' | 'expired'
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
  status: 'pending' | 'running' | 'done' | 'failed' | string
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

export interface ConnectionSchema {
  connectionId: number
  databases: {
    name: string
    schemas?: { name: string; tables: { name: string }[] }[]
    tables: { name: string }[]
  }[]
  error?: string
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

export interface SettingsResp {
  settings: Record<string, string>
  webhook: WebhookConfig | null
  strictMode: boolean
  // Secret values are never returned; these flags say whether one is on file so
  // the UI can show "已配置" and treat a blank input as "keep unchanged".
  secretsSet?: Record<string, boolean>
  webhookHasSecret?: boolean
}
