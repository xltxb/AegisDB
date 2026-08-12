import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import api from '@/api'
import type { SnippetLimits, TerminalSnippet } from '@/types'

/**
 * The operator's saved terminal scripts, held here rather than per session.
 *
 * Every terminal tab is its own component with its own xterm, and all of them
 * answer to the same hotkeys. Kept as component state, editing a snippet in one
 * tab would leave the others firing the previous text — the shortcut would run
 * something other than what the list in front of you says it runs, which is the
 * one thing a hotkey must never do.
 */
export const useSnippetStore = defineStore('snippets', () => {
  const items = ref<TerminalSnippet[]>([])
  const limits = ref<SnippetLimits>({ maxBytes: 8192, maxName: 64, maxSlot: 9, max: 50 })
  const loaded = ref(false)
  let inflight: Promise<void> | null = null

  /** slot (1-9) -> snippet, for the hotkey lookup. */
  const bySlot = computed(() => {
    const m: Record<number, TerminalSnippet> = {}
    for (const s of items.value) if (s.slot > 0) m[s.slot] = s
    return m
  })

  async function load(force = false): Promise<void> {
    if (loaded.value && !force) return
    if (inflight) return inflight
    inflight = (async () => {
      const [list, lim] = await Promise.all([api.snippets(), api.snippetLimits().catch(() => limits.value)])
      items.value = list
      limits.value = lim
      loaded.value = true
    })().finally(() => { inflight = null })
    return inflight
  }

  /** Re-read after any write: binding a hotkey releases whichever snippet held
   *  it, so the row that changed is not always the row that was saved. */
  const refresh = () => load(true)

  return { items, limits, loaded, bySlot, load, refresh }
})
