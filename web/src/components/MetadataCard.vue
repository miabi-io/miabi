<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    metadata?: Record<string, string> | null
    title?: string
    // When false, no key is treated as platform-managed: keys render verbatim
    // with no lock icon or prefix stripping. Used for annotations, which have no
    // reserved-key concept (a "miabi.io/" annotation is just a user key).
    reserved?: boolean
  }>(),
  { title: '', reserved: true },
)

const RESERVED = 'miabi.io/'

// Owner keys are surfaced separately as a linkable chip (OwnerChip), so they are
// hidden from the generic metadata list to avoid showing them twice.
const HIDDEN = new Set(['miabi.io/owner-kind', 'miabi.io/owner-id', 'miabi.io/owner-name'])

interface Row {
  key: string
  display: string
  value: string
  builtin: boolean
}

// Built-in (reserved) keys first, then user labels; both alphabetical.
const rows = computed<Row[]>(() => {
  const md = props.metadata || {}
  return Object.keys(md)
    .filter((key) => !HIDDEN.has(key))
    .map((key): Row => {
      const builtin = props.reserved && key.startsWith(RESERVED)
      return { key, display: builtin ? key.slice(RESERVED.length) : key, value: md[key], builtin }
    })
    .sort((a, b) => Number(b.builtin) - Number(a.builtin) || a.display.localeCompare(b.display))
})
</script>

<template>
  <div v-if="rows.length" class="card">
    <div class="card-header">
      <h3>{{ title || $t('metadata.metadata') }}</h3>
    </div>
    <div class="card-body">
      <dl class="meta-list">
        <div v-for="r in rows" :key="r.key" class="meta-row">
          <dt>
            <span v-if="r.builtin" class="mdi mdi-lock-outline meta-builtin" :title="$t('metadata.builtInPlatformManagedRead')"></span>
            <span class="meta-key">{{ r.display }}</span>
          </dt>
          <dd class="meta-value">{{ r.value }}</dd>
        </div>
      </dl>
    </div>
  </div>
</template>

<style scoped>
.meta-list {
  margin: 0;
  display: flex;
  flex-direction: column;
}
.meta-row {
  display: grid;
  grid-template-columns: minmax(140px, 240px) 1fr;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border-primary);
  align-items: baseline;
}
.meta-row:last-child {
  border-bottom: none;
}
.meta-row dt {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.meta-key {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  color: var(--text-muted);
  word-break: break-all;
}
.meta-builtin {
  font-size: 14px;
  color: var(--text-muted);
  flex-shrink: 0;
}
.meta-row dd {
  margin: 0;
  font-size: 13px;
  color: var(--text-primary);
  word-break: break-word;
}
.meta-value {
  font-family: var(--font-mono, monospace);
}
</style>
