import clsx from 'clsx'

/** 开关:44×24(原型全局统一尺寸)。 */
export function Switch({
  checked, onChange, disabled,
}: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      className={clsx('c-switch', checked && 'on')}
      onClick={() => !disabled && onChange(!checked)}
    >
      <span className="c-switch-knob" />
    </button>
  )
}
