<script setup lang="ts">
import { computed } from 'vue'
import type { Pageable } from '@/api/types'

const props = defineProps<{ pageable: Pageable }>()
const emit = defineEmits<{ (e: 'page', page: number): void }>()

const currentPage = computed(() => props.pageable.current_page)
const totalPages = computed(() => props.pageable.total_pages)

// First page, last page, and a window around the current page, with nulls for
// ellipsis gaps.
const visiblePages = computed<(number | null)[]>(() => {
  const total = totalPages.value
  const current = currentPage.value
  if (total <= 7) return Array.from({ length: total }, (_, i) => i)

  const pages: (number | null)[] = [0]
  const start = Math.max(1, current - 1)
  const end = Math.min(total - 2, current + 1)
  if (start > 1) pages.push(null)
  for (let i = start; i <= end; i++) pages.push(i)
  if (end < total - 2) pages.push(null)
  pages.push(total - 1)
  return pages
})
</script>

<template>
  <div v-if="totalPages > 1" class="pagination">
    <span class="pagination-info">
      Page {{ currentPage + 1 }} of {{ totalPages }} · {{ pageable.total_elements }} total
    </span>
    <div class="pagination-buttons">
      <button class="btn btn-secondary btn-sm" :disabled="currentPage === 0" :aria-label="$t('marketplace.previousPage')" @click="emit('page', currentPage - 1)">
        <span class="mdi mdi-chevron-left"></span>
      </button>
      <template v-for="(p, idx) in visiblePages" :key="idx">
        <span v-if="p === null" class="pagination-ellipsis">…</span>
        <button
          v-else
          class="btn btn-sm"
          :class="p === currentPage ? 'btn-primary' : 'btn-secondary'"
          @click="emit('page', p)"
        >
          {{ p + 1 }}
        </button>
      </template>
      <button class="btn btn-secondary btn-sm" :disabled="currentPage >= totalPages - 1" :aria-label="$t('marketplace.nextPage')" @click="emit('page', currentPage + 1)">
        <span class="mdi mdi-chevron-right"></span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding: 14px 4px 2px;
}
.pagination-info {
  font-size: 13px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}
.pagination-buttons {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.pagination-buttons .btn-sm {
  min-width: 32px;
  justify-content: center;
}
.pagination-ellipsis {
  padding: 0 4px;
  color: var(--text-muted);
}
</style>
