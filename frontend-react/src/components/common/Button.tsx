import clsx from 'clsx'
import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { useCan } from '@/hooks/useCan'

type Variant = 'primary' | 'secondary' | 'danger' | 'ghost'

interface Props extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  /** `能力:分层`。判 deny 时禁用并给出原因,不隐藏。 */
  capability?: string
  children: ReactNode
}

export function Button({ variant = 'secondary', capability, disabled, children, ...rest }: Props) {
  const can = useCan()
  const denied = capability ? !can(capability) : false
  return (
    <button
      {...rest}
      disabled={disabled || denied}
      title={denied ? '你的角色在该分层没有此项能力' : rest.title}
      className={clsx('c-btn', `v-${variant}`, denied && 'is-denied', rest.className)}
    >
      {children}
    </button>
  )
}
