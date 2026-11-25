import { useEffect, useState } from 'react'
import { setUnsaved } from '@/lib/unsavedChanges'

/** 登记"这页脏不脏"。变 true 即登记、变 false 即注销,卸载时也注销(见 lib/unsavedChanges)。 */
export function useUnsavedFlag(dirty: boolean) {
  const [id] = useState(() => Symbol('unsaved'))
  useEffect(() => { setUnsaved(id, dirty) }, [id, dirty])
  useEffect(() => () => setUnsaved(id, false), [id])
}
