import clsx from 'clsx'

export interface SegOption<T extends string> { value: T; label: string }

/** 分段控件:外框 radius 10 · 每段 32px · 段间 1px 分隔(原型规格)。 */
export function Segmented<T extends string>({
  value, options, onChange,
}: { value: T; options: SegOption<T>[]; onChange: (v: T) => void }) {
  return (
    <div className="c-seg">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          className={clsx('c-seg-item', value === o.value && 'on')}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
