// confirmAction gates a destructive/high-risk operation behind an explicit
// user confirmation (M15). Kept as a thin wrapper over the native dialog so it
// can be swapped for a styled modal later without touching call sites.
export function confirmAction(message: string): boolean {
  return typeof window === 'undefined' ? true : window.confirm(message)
}
