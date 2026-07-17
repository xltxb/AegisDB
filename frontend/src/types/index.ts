// Front/back contract types (mirror backend dto package).

export type Env = 'prod' | 'staging' | 'dev'
export type CapLevel = 'allow' | 'approve' | 'deny'
export type RiskLevel = 'high' | 'mid' | 'off' | 'low'
export type MenuKey = 'terminal' | 'approve' | 'db' | 'rules' | 'perms' | 'audit' | 'settings'

export interface Me {
  id: number
  name: string
  email: string
  initials: string
  roleId: number
  roleCode: string
  roleName: string
  layer: string
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
  status: 'active' | 'disabled' | 'invited'
  mfaEnabled: boolean
  lastActive: string
}

export interface RiskCommandView {
  command: string
  env: Record<string, string> // prod/staging/dev -> high|mid|off
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
  env: string
  instance: string
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
  command: string
  risk: string
  result: string
  approvalNo: string
  hash: string
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

export interface ScriptUpload {
  id: number
  userId: number
  filename: string
  path: string
  size: number
  source: 'upload' | 'terminal' | string
  createdAt: string
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
  databases: { name: string; tables: { name: string }[] }[]
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
