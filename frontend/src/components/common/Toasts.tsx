// Global toast host — renders transient success/error/info messages pushed via
// the ui store's notify()/notifyError(). Mounted once at the app root.
import { useUIStore } from '@/stores/ui'
import './Toasts.scoped.css'

export default function Toasts() {
  const toasts = useUIStore((s) => s.toasts)
  const dismiss = useUIStore((s) => s.dismissToast)
  return (
    <div className="toast-host" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast--${t.kind}`} role="status" onClick={() => dismiss(t.id)}>{t.msg}</div>
      ))}
    </div>
  )
}
