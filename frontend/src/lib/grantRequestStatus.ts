// 数据查询权限申请的状态(issue #188)。
import type { QueryGrantRequest } from '@/types'

/**
 * 申请状态 → 词条键。用 Record 而不是 t('qaSt_' + status):状态联合里新增一种,这里不补编译就
 * 不过,不会在界面上露出键名(issue #188)。
 */
export const GRANT_REQUEST_STATUS_LABEL: Record<QueryGrantRequest['status'], string> = {
  pending: 'qaSt_pending',
  approved: 'qaSt_approved',
  failed: 'qaSt_failed',
  rejected: 'qaSt_rejected',
  cancelled: 'qaSt_cancelled',
  expired: 'qaSt_expired',
}
