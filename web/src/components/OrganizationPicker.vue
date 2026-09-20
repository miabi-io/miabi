<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi } from '@/api/admin'
import type { Organization } from '@/api/types'

/**
 * Picks one organization.
 *
 * A combobox rather than a <select> because an operator running many tenants has many of them, and
 * scanning a long native dropdown for "which Acme was it" is the moment this field gets skipped. The
 * whole list arrives in one call — organizations are few enough to hold and there is no paged
 * endpoint — so the filtering is local and instant.
 *
 * v-model is the organization id; 0 means the default organization, which is what a null
 * organization_id resolves to everywhere in the product.
 */
const { t } = useI18n()
const props = withDefaults(
  defineProps<{
    modelValue: number
    label?: string
    disabled?: boolean
    /** Label for id 0. "Default organization" when creating; callers editing an existing row may
     *  prefer to name the actual default org. */
    defaultLabel?: string
  }>(),
  { label: '', disabled: false, defaultLabel: '' },
)
const emit = defineEmits<{ (e: 'update:modelValue', v: number): void }>()

const orgs = ref<Organization[]>([])
const loaded = ref(false)
const open = ref(false)
const query = ref('')
const active = ref(0)
const root = ref<HTMLElement | null>(null)
const input = ref<HTMLInputElement | null>(null)

async function load() {
  if (loaded.value) return
  try {
    orgs.value = (await adminApi.listOrganizations()).data.data ?? []
  } catch {
    // The form still works without the list: the field falls back to the default organization.
  } finally {
    loaded.value = true
  }
}

const selected = computed(() => orgs.value.find((o) => o.id === props.modelValue) ?? null)
const selectedLabel = computed(() =>
  props.modelValue === 0 ? (props.defaultLabel || t('orgPicker.defaultOrganization')) : (selected.value?.display_name || selected.value?.name || `#${props.modelValue}`),
)

// The default organization is offered as a real row so "put them back on the default" is a choice,
// not an empty field.
interface Choice { id: number; label: string; hint: string }
const choices = computed<Choice[]>(() => {
  const all: Choice[] = [{ id: 0, label: props.defaultLabel || t('orgPicker.defaultOrganization'), hint: '' }]
  for (const o of orgs.value) {
    all.push({
      id: o.id,
      label: o.display_name || o.name,
      hint: o.max_workspaces < 0 ? `${o.name} · ${o.workspace_count} workspaces` : `${o.name} · ${o.workspace_count} of ${o.max_workspaces}`,
    })
  }
  const q = query.value.trim().toLowerCase()
  if (!q) return all
  return all.filter((c) => c.label.toLowerCase().includes(q) || c.hint.toLowerCase().includes(q))
})

async function show() {
  if (props.disabled) return
  await load()
  query.value = ''
  active.value = 0
  open.value = true
  await nextTick()
  input.value?.focus()
}

function choose(c: Choice) {
  emit('update:modelValue', c.id)
  open.value = false
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    open.value = false
    return
  }
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const n = choices.value.length
    if (!n) return
    active.value = (active.value + (e.key === 'ArrowDown' ? 1 : n - 1)) % n
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    const c = choices.value[active.value]
    if (c) choose(c)
  }
}

function onDocClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}
watch(open, (v) => {
  if (v) document.addEventListener('mousedown', onDocClick)
  else document.removeEventListener('mousedown', onDocClick)
})
onBeforeUnmount(() => document.removeEventListener('mousedown', onDocClick))

// Resolving the current value's label needs the list even before the field is opened.
watch(() => props.modelValue, (v) => { if (v !== 0) void load() }, { immediate: true })
</script>

<template>
  <div ref="root" class="org-picker">
    <label class="form-label">{{ label || $t('orgPicker.organization') }}</label>
    <button v-if="!open" type="button" class="form-input org-picker-value" :disabled="disabled" @click="show">
      <span>{{ selectedLabel }}</span>
      <span class="mdi mdi-menu-down"></span>
    </button>
    <template v-else>
      <input
        ref="input"
        v-model="query"
        class="form-input"
        type="text"
        :placeholder="$t('orgPicker.searchOrganizations')"
        @keydown="onKey"
      />
      <ul class="org-picker-list">
        <li v-if="!choices.length" class="org-picker-empty">No organization matches “{{ query }}”</li>
        <li
          v-for="(c, i) in choices"
          :key="c.id"
          :class="['org-picker-item', { active: i === active, selected: c.id === modelValue }]"
          @mouseenter="active = i"
          @mousedown.prevent="choose(c)"
        >
          <span class="org-picker-label">{{ c.label }}</span>
          <span v-if="c.hint" class="org-picker-hint mono">{{ c.hint }}</span>
        </li>
      </ul>
    </template>
  </div>
</template>

<style scoped>
.org-picker {
  position: relative;
}
.org-picker-value {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  text-align: left;
  cursor: pointer;
}
.org-picker-value:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.org-picker-list {
  position: absolute;
  z-index: 20;
  left: 0;
  right: 0;
  margin: 4px 0 0;
  padding: 4px;
  list-style: none;
  max-height: 240px;
  overflow-y: auto;
  background: var(--bg-elevated, var(--bg-card));
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgb(0 0 0 / 18%);
}
.org-picker-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 7px 10px;
  border-radius: 6px;
  cursor: pointer;
}
.org-picker-item.active {
  background: var(--bg-hover, rgb(127 127 127 / 12%));
}
.org-picker-item.selected .org-picker-label {
  font-weight: 600;
}
.org-picker-hint {
  font-size: 12px;
  color: var(--text-muted);
}
.org-picker-empty {
  padding: 8px 10px;
  color: var(--text-muted);
  font-size: 13px;
}
</style>
