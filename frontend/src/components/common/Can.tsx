import type { ReactNode } from 'react'
import { useCan } from '@/hooks/useCan'

interface CanProps {
  /** `能力:分层`,如 `ddl:prod`。 */
  capability: string
  fallback?: ReactNode
  children: ReactNode
}

/**
 * 需要**藏掉**整块入口时用它;只是让按钮点不动,直接给 Button 传 disabled 更好 ——
 * 用户看得到按钮,也就问得出为什么点不动。
 */
export function Can({ capability, fallback = null, children }: CanProps) {
  return useCan()(capability) ? <>{children}</> : <>{fallback}</>
}

export default Can
