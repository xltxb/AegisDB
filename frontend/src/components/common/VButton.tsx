import type { ButtonHTMLAttributes } from 'react'
import clsx from 'clsx'
import './VButton.scoped.css'

interface Props extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'danger'
  fullWidth?: boolean
  height?: string
}

export default function VButton({ variant = 'secondary', fullWidth = false, height = '40px', className, style, children, ...rest }: Props) {
  return (
    <button {...rest} className={clsx('vbtn', `v-${variant}`, fullWidth && 'full', className)} style={{ height, ...style }}>
      {children}
    </button>
  )
}
