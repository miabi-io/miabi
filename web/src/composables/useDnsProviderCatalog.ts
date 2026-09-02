import { ref } from 'vue'
import { dnsProviderApi } from '@/api/dns'
import type { DNSProviderDescriptor } from '@/api/types'

let cached: DNSProviderDescriptor[] | null = null
const catalog = ref<DNSProviderDescriptor[] | null>(cached)
let inflight: Promise<void> | null = null

export function useDnsProviderCatalog() {
  async function ensure(workspaceId: number | null | undefined) {
    if (catalog.value || !workspaceId) return
    if (!inflight) {
      inflight = dnsProviderApi
        .catalog(workspaceId)
        .then((res) => {
          cached = res.data.data ?? []
          catalog.value = cached
        })
        .catch(() => {})
        .finally(() => {
          inflight = null
        })
    }
    return inflight
  }

  function describe(type: string): DNSProviderDescriptor | null {
    return catalog.value?.find((d) => d.type === type) ?? null
  }

  function label(type: string): string {
    return describe(type)?.label ?? type
  }

  return { catalog, ensure, describe, label }
}
