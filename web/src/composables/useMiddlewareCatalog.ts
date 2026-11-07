import { ref } from 'vue'
import { middlewareApi } from '@/api/middlewares'
import type { MiddlewareCatalog, MiddlewareDescriptor } from '@/api/types'

// Fetched once per session from the server catalog, so a new backend type appears everywhere with no frontend change.
let cached: MiddlewareCatalog | null = null
const catalog = ref<MiddlewareCatalog | null>(cached)
let inflight: Promise<void> | null = null

export function useMiddlewareCatalog() {
  async function ensure(workspaceId: number | null | undefined) {
    if (catalog.value || !workspaceId) return
    if (!inflight) {
      inflight = middlewareApi
        .catalog(workspaceId)
        .then((res) => {
          cached = res.data.data
          catalog.value = cached
        })
        .catch(() => {
          /* leave uncached; callers fall back to the raw type string */
        })
        .finally(() => {
          inflight = null
        })
    }
    return inflight
  }

  function typeInfo(type: string): MiddlewareDescriptor | undefined {
    return catalog.value?.types.find((d) => d.type === type)
  }

  // Falls back to the raw type when the catalog hasn't loaded or the type is uncatalogued.
  function typeLabel(type: string): string {
    return typeInfo(type)?.display_name ?? type
  }

  return { catalog, ensure, typeInfo, typeLabel }
}
