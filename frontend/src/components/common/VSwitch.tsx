import type { ButtonHTMLAttributes } from 'react'
import clsx from 'clsx'
import './VSwitch.scoped.css'

interface Props extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'value' | 'onChange'> {
  value: boolean
  onChange?: (v: boolean) => void
}

export default function VSwitch({ value, onChange, className, onClick, ...rest }: Props) {
  return (
    <button
      {...rest}
      type="button"
      className={clsx('vsw', value && 'on', className)}
      onClick={(e) => { onChange?.(!value); onClick?.(e) }}
    >
      <span className="knob" />
    </button>
  )
}
