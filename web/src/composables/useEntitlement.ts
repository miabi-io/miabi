import { computed, type ComputedRef } from 'vue'
import { useLicenseStore } from '@/stores/license'
import type { LicenseEdition } from '@/api/types'

export interface Entitlement {
  // has: the feature is licensed and usable at runtime (false in Community).
  has: ComputedRef<boolean>
  // False once the license degrades past its grace period: paid config goes read-only on expiry.
  mutable: ComputedRef<boolean>
  edition: ComputedRef<LicenseEdition>
}

// useEntitlement gates UI on a license flag: show a lock when !has, read-only when has && !mutable.
export function useEntitlement(flag: string): Entitlement {
  const store = useLicenseStore()
  return {
    has: computed(() => store.has(flag)),
    mutable: computed(() => store.mutable(flag)),
    edition: computed(() => store.edition),
  }
}
