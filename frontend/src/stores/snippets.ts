import { create } from 'zustand'
import api from '@/api'
import type { SnippetLimits, TerminalSnippet } from '@/types'

interface SnippetState {
  items: TerminalSnippet[]
  limits: SnippetLimits
  loaded: boolean
  /** slot (1-9) -> snippet, for the hotkey lookup. */
  bySlot: Record<number, TerminalSnippet>
  load(force?: boolean): Promise<void>
  /** Re-read after any write: binding a hotkey releases whichever snippet held
   *  it, so the row that changed is not always the row that was saved. */
  refresh(): Promise<void>
}

const slots = (items: TerminalSnippet[]) => {
  const m: Record<number, TerminalSnippet> = {}
  for (const s of items) if (s.slot > 0) m[s.slot] = s
  return m
}

/**
 * The operator's saved terminal scripts, held here rather than per session.
 *
 * Every terminal tab is its own component with its own xterm, and all of them
 * answer to the same hotkeys. Kept as component state, editing a snippet in one
 * tab would leave the others firing the previous text — the shortcut would run
 * something other than what the list in front of you says it runs, which is the
 * one thing a hotkey must never do.
 */
export const useSnippetStore = create<SnippetState>()((set, get) => {
  let inflight: Promise<void> | null = null
  return {
    items: [],
    limits: { maxBytes: 8192, maxName: 64, maxSlot: 9, max: 50 },
    loaded: false,
    bySlot: {},
    async load(force = false) {
      if (get().loaded && !force) return
      if (inflight) return inflight
      inflight = (async () => {
        const [items, limits] = await Promise.all([api.snippets(), api.snippetLimits().catch(() => get().limits)])
        set({ items, limits, bySlot: slots(items), loaded: true })
      })().finally(() => { inflight = null })
      return inflight
    },
    refresh: () => get().load(true),
  }
})
