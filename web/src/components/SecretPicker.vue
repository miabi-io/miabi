<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { secretApi, type SecretOwnership } from '@/api/secrets'
import type { Secret } from '@/api/types'

/**
 * Picks one secret by name from the workspace vault.
 *
 * A plain <select> does not work here: the vault is paged, so a dropdown can
 * only ever show the first page and offers no way to reach the rest. This is a
 * combobox instead — you type, the server searches every page, and the list
 * shows what matched. The ownership filter is sent to the API for the same
 * reason: narrowing a single page in the browser would hide secrets that exist
 * on later ones.
 */
const props = withDefaults(
  defineProps<{
    /** Selected secret name ('' = nothing chosen). */
    modelValue?: string
    label?: string
    /** Ownership shown first. Callers picking a credential default to
     *  'unmanaged': a managed secret belongs to another resource and rotates
     *  with it, so it is rarely what you want to point a credential at. */
    defaultOwnership?: SecretOwnership
    disabled?: boolean
  }>(),
  { modelValue: '', label: 'Secret', defaultOwnership: 'all', disabled: false },
)
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const ws = useWorkspaceStore()
const { currentWorkspaceId } = storeToRefs(ws)

const PAGE_SIZE = 20

const query = ref('')
const ownership = ref<SecretOwnership>(props.defaultOwnership)
const results = ref<Secret[]>([])
const total = ref(0)
const loading = ref(false)
const failed = ref(false)
const open = ref(false)
const activeIndex = ref(-1)
const root = ref<HTMLElement | null>(null)

const selected = computed(() => props.modelValue)
// Everything matching is on screen when the first page covers the total.
const hiddenCount = computed(() => Math.max(0, total.value - results.value.length))

async function search() {
  const id = currentWorkspaceId.value
  if (!id) { results.value = []; total.value = 0; return }
  loading.value = true
  failed.value = false
  try {
    const res = await secretApi.list(id, query.value.trim(), 0, PAGE_SIZE, ownership.value)
    results.value = res.data.data ?? []
    total.value = res.data.pageable?.total_elements ?? results.value.length
    activeIndex.value = results.value.length ? 0 : -1
  } catch {
    failed.value = true
    results.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

// Debounced so typing doesn't fire a request per keystroke; the filter chips
// re-query immediately, since that is a deliberate single action.
let timer: ReturnType<typeof setTimeout> | undefined
function onInput() {
  open.value = true
  if (timer) clearTimeout(timer)
  timer = setTimeout(search, 250)
}
watch(ownership, search)
watch(currentWorkspaceId, () => { results.value = []; search() })

onMounted(() => {
  search()
  document.addEventListener('click', onDocumentClick)
})
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  document.removeEventListener('click', onDocumentClick)
})

function onDocumentClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}

function choose(s: Secret) {
  emit('update:modelValue', s.name)
  query.value = ''
  open.value = false
}
function clear() {
  emit('update:modelValue', '')
  open.value = true
}

function onKeydown(e: KeyboardEvent) {
  if (!open.value && (e.key === 'ArrowDown' || e.key === 'Enter')) { open.value = true; return }
  if (e.key === 'ArrowDown') { e.preventDefault(); activeIndex.value = Math.min(activeIndex.value + 1, results.value.length - 1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); activeIndex.value = Math.max(activeIndex.value - 1, 0) }
  else if (e.key === 'Enter') {
    const hit = results.value[activeIndex.value]
    if (hit) { e.preventDefault(); choose(hit) }
  } else if (e.key === 'Escape') { open.value = false }
}

const filters: Array<{ value: SecretOwnership; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'unmanaged', label: 'Not managed' },
  { value: 'managed', label: 'Managed' },
]
</script>

<template>
  <div ref="root" class="secret-picker">
    <!-- The chosen secret replaces the input, so the current value is never in
         doubt and clearing it is one click. -->
    <div v-if="selected" class="chosen">
      <span class="mdi mdi-key-variant"></span>
      <span class="chosen-name">{{ selected }}</span>
      <button v-if="!disabled" type="button" class="btn-icon btn-icon-muted" title="Choose another secret"
        aria-label="Choose another secret" @click="clear">
        <span class="mdi mdi-close"></span>
      </button>
    </div>

    <template v-else>
      <input
        v-model="query"
        type="text"
        class="form-input"
        :placeholder="`Search ${label.toLowerCase()}s by name or description…`"
        :aria-label="label"
        :disabled="disabled"
        autocomplete="off"
        role="combobox"
        :aria-expanded="open"
        @input="onInput"
        @focus="open = true"
        @keydown="onKeydown"
      />

      <div v-if="open" class="results">
        <div class="filters">
          <button v-for="f in filters" :key="f.value" type="button" class="chip"
            :class="{ active: ownership === f.value }" @click="ownership = f.value">{{ f.label }}</button>
        </div>

        <div v-if="loading" class="note"><span class="spinner spinner-sm"></span> Searching…</div>
        <div v-else-if="failed" class="note">Could not load secrets.</div>
        <div v-else-if="results.length === 0" class="note">
          {{ query.trim() ? `No secrets match “${query.trim()}”.` : 'No secrets in this workspace yet.' }}
        </div>
        <ul v-else class="options" role="listbox">
          <li v-for="(s, i) in results" :key="s.id" role="option" :aria-selected="i === activeIndex"
            class="option" :class="{ active: i === activeIndex }"
            @mouseenter="activeIndex = i" @click="choose(s)">
            <span class="option-name">{{ s.name }}</span>
            <span v-if="s.managed" class="badge badge-muted">managed</span>
            <span v-if="s.description" class="option-desc">{{ s.description }}</span>
          </li>
        </ul>
        <div v-if="hiddenCount > 0" class="note">
          {{ hiddenCount }} more — keep typing to narrow the list.
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.secret-picker { position: relative; }
.chosen {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 10px; border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg-subtle, transparent);
}
.chosen-name { font-family: monospace; font-size: 13px; flex: 1; overflow-wrap: anywhere; }
.results {
  position: absolute; z-index: 20; left: 0; right: 0; margin-top: 4px;
  border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg-elevated, var(--bg, #fff));
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  max-height: 320px; overflow-y: auto;
}
[data-theme="dark"] .results {
  background: var(--bg-elevated, var(--bg, #1a1a2e));
}
.filters { display: flex; gap: 6px; padding: 8px; border-bottom: 1px solid var(--border); }
.chip {
  padding: 3px 10px; font-size: 12px; border-radius: 999px; cursor: pointer;
  border: 1px solid var(--border); background: transparent; color: var(--text-muted);
}
.chip.active { border-color: var(--primary); color: var(--primary); }
.options { list-style: none; margin: 0; padding: 4px; }
.option {
  display: flex; align-items: center; gap: 8px;
  padding: 7px 8px; border-radius: 4px; cursor: pointer;
}
.option.active { background: var(--bg-hover, rgba(127, 127, 127, 0.12)); }
.option-name { font-family: monospace; font-size: 13px; }
.option-desc {
  font-size: 12px; color: var(--text-muted); margin-left: auto;
  max-width: 55%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.note { padding: 10px; font-size: 12px; color: var(--text-muted); }
.spinner-sm { width: 12px; height: 12px; vertical-align: middle; }
</style>
