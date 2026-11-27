// 审批单类型 → 显示名的词条键(issue #188)。
//
// 用 Record 而不是 t('apType_' + type):类型联合里新增一种,这里不补编译就不过;拼出来的键
// 缺了词条不会报错,界面上直接露出键名。筛选项也从这张表生成,不再手写一份去漏掉哪一种。
import type { Approval } from '@/types'

export type ApprovalType = NonNullable<Approval['type']>

export const APPROVAL_TYPE_LABEL: Record<ApprovalType, string> = {
  cmd: 'apType_cmd',
  release: 'apType_release',
  window: 'apType_window',
  export: 'apType_export',
  osc: 'apType_osc',
  grant: 'apType_grant',
}

/** 按上表的顺序列出全部类型(筛选下拉框的顺序)。 */
export const APPROVAL_TYPES = Object.keys(APPROVAL_TYPE_LABEL) as ApprovalType[]
