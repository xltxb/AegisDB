import { useObservable } from '@/hooks/useObservable'
import { brandState, type Branding } from '@/lib/brand'

export function useBrand(): Branding {
  return useObservable(brandState)
}
