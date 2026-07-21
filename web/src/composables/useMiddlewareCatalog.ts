import { ref } from 'vue'
import { middlewareApi } from '@/api/middlewares'
import type { MiddlewareCatalog, MiddlewareDescriptor } from '@/api/types'

// Single source of truth for middleware type metadata on the client: the server
// catalog. Fetched once per session and cached across components, so the list,
// detail and form views all describe a type the same way — and a new backend
// type appears everywhere with no frontend change.
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

  // typeLabel is the human display name, falling back to the raw type when the
  // catalog hasn't loaded or the type is uncatalogued (an advanced passthrough).
  function typeLabel(type: string): string {
    return typeInfo(type)?.display_name ?? type
  }

  return { catalog, ensure, typeInfo, typeLabel }
}
