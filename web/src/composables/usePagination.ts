import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { Pageable } from '@/api/types'

// ?page= is 1-indexed for readability; the internal page is 0-indexed.
export function usePagination(fetchFn: (page: number) => Promise<void>) {
  const route = useRoute()
  const router = useRouter()
  const pageable = ref<Pageable>({
    current_page: 0,
    size: 20,
    total_pages: 0,
    total_elements: 0,
    empty: true,
  })

  async function goToPage(page: number) {
    if (page < 0) page = 0
    router.replace({ query: { ...route.query, page: page > 0 ? String(page + 1) : undefined } })
    await fetchFn(page)
  }

  onMounted(() => {
    const p = Number(route.query.page)
    fetchFn(p > 0 ? p - 1 : 0)
  })

  return { pageable, goToPage }
}
