<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import type { MiddlewareField } from '@/api/types'
import GenerateButton from '@/components/GenerateButton.vue'

// Recursive renderer for one middleware rule field. It mutates model[field.key]
// in place (matching the form's direct-binding style), and recurses for nested
// object groups and list rows — so header maps, CORS blocks, cookies and the
// error-interceptor list are all editable from the form with no raw YAML.
//
// Column labels, placeholders and the "+ Add" noun come from the schema, not
// from this file: a setHeaders row is "Header → value" while a JWT
// forwardHeaders row is "Header → claim path", and one hardcoded placeholder for
// both taught the wrong thing about whichever it didn't describe.
const props = defineProps<{
  field: MiddlewareField
  model: Record<string, any>
  editing: boolean
}>()

const f = props.field

// --- container defaults: ensure the model holds the right shape to bind to ---
function ensureShape() {
  const v = props.model[f.key]
  if (f.type === 'map' && (typeof v !== 'object' || v === null || Array.isArray(v))) props.model[f.key] = {}
  else if (f.type === 'object' && (typeof v !== 'object' || v === null || Array.isArray(v))) props.model[f.key] = {}
  else if ((f.type === 'list' || f.type === 'users' || f.type === 'pairs') && !Array.isArray(v)) props.model[f.key] = []
}
ensureShape()

const addNoun = computed(() => f.add_label || f.label.toLowerCase())

// --- users editor (basicAuth) ---
function userRows(): Array<{ username: string; password: string }> {
  return (props.model[f.key] as Array<{ username: string; password: string }>) || []
}
function addUser() {
  ;(props.model[f.key] ||= []).push({ username: '', password: '' })
}
function removeUser(i: number) {
  userRows().splice(i, 1)
}

// --- map editor (map<string,string>) ---
// Kept as an ordered pair list locally; written back to the model object on edit
// so key renames and empty values (the "delete header" sentinel) round-trip.
const pairs = reactive<Array<{ k: string; v: string }>>(
  f.type === 'map' ? Object.entries(props.model[f.key] || {}).map(([k, v]) => ({ k, v: String(v) })) : [],
)
function syncMap() {
  const out: Record<string, string> = {}
  for (const p of pairs) if (p.k.trim()) out[p.k.trim()] = p.v
  props.model[f.key] = out
}
function addPair() {
  pairs.push({ k: '', v: '' })
}
function removePair(i: number) {
  pairs.splice(i, 1)
  syncMap()
}
watch(pairs, syncMap, { deep: true })

// --- mapping editor (Goma's "source: target" string list) ---
// Stored as strings because that is Goma's wire format, but edited as two
// inputs — the colon convention is an implementation detail, not something a
// form should make you know. A blank target means "keep the same name".
const maps = reactive<Array<{ from: string; to: string }>>(
  f.type === 'pairs'
    ? ((props.model[f.key] as string[]) || []).map((s) => {
        const i = String(s).indexOf(':')
        return i < 0
          ? { from: String(s).trim(), to: '' }
          : { from: String(s).slice(0, i).trim(), to: String(s).slice(i + 1).trim() }
      })
    : [],
)
function syncMaps() {
  props.model[f.key] = maps
    .filter((m) => m.from.trim())
    .map((m) => (m.to.trim() ? `${m.from.trim()}: ${m.to.trim()}` : m.from.trim()))
}
function addMapping() {
  maps.push({ from: '', to: '' })
}
function removeMapping(i: number) {
  maps.splice(i, 1)
  syncMaps()
}
watch(maps, syncMaps, { deep: true })

// --- list editor ([]object) ---
function rows(): Array<Record<string, any>> {
  return (props.model[f.key] as Array<Record<string, any>>) || []
}
function addRow() {
  const row: Record<string, any> = {}
  for (const sub of f.fields ?? []) {
    if (sub.type === 'list' || sub.type === 'pairs') row[sub.key] = []
    else if (sub.type === 'map' || sub.type === 'object') row[sub.key] = {}
    else if (sub.default !== undefined && sub.default !== null) row[sub.key] = sub.default
  }
  ;(props.model[f.key] ||= []).push(row)
}
function removeRow(i: number) {
  rows().splice(i, 1)
}

// --- tag editor (string[] / int[]) ---
// Entries are chips, not a comma-separated string. The old version re-rendered
// the input from the parsed array on every keystroke, so typing a comma parsed
// to a trailing empty entry, got filtered out, and the comma vanished as you
// typed it — you could not enter a second value at all.
const draft = ref('')
function tags(): Array<string | number> {
  const v = props.model[f.key]
  return Array.isArray(v) ? v : []
}
function commitDraft() {
  const parts = draft.value
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)
  if (!parts.length) {
    draft.value = ''
    return
  }
  const existing = tags().map(String)
  const parsed: Array<string | number> =
    f.type === 'int[]' ? parts.map(Number).filter((n) => !Number.isNaN(n)) : parts
  // Adding the same value twice is always a mistake, and silently ignoring it
  // beats a duplicate chip the user has to hunt down.
  const add = parsed.filter((x) => !existing.includes(String(x)))
  if (add.length) props.model[f.key] = [...tags(), ...add]
  draft.value = ''
}
function onTagKey(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ',') {
    e.preventDefault()
    commitDraft()
  } else if (e.key === 'Backspace' && draft.value === '' && tags().length) {
    // Backspace on an empty box removes the last chip, as every tag input does.
    props.model[f.key] = tags().slice(0, -1)
  }
}
// Pasting a list ("10.0.0.0/8, 192.168.0.0/16") should become chips, not one entry.
function onTagPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text') ?? ''
  if (!text.includes(',') && !text.includes('\n')) return
  e.preventDefault()
  draft.value = text.replace(/\n/g, ',')
  commitDraft()
}
function removeTag(i: number) {
  const next = tags().slice()
  next.splice(i, 1)
  props.model[f.key] = next
}
</script>

<template>
  <div class="mw-field">
    <label v-if="f.type !== 'bool'" class="form-label">
      {{ f.label }}<span v-if="f.required" class="req">*</span>
      <span v-if="f.secret" class="mdi mdi-lock-outline secret-ico" title="Stored encrypted"></span>
    </label>

    <!-- key/value map (setHeaders, jwt forwardHeaders) -->
    <template v-if="f.type === 'map'">
      <div v-if="pairs.length" class="kv-head">
        <span>{{ f.key_label || 'Key' }}</span>
        <span>{{ f.value_label || 'Value' }}</span>
      </div>
      <div v-for="(p, i) in pairs" :key="i" class="kv-row">
        <input v-model="p.k" class="form-input" :placeholder="f.key_placeholder || ''" :aria-label="f.key_label || 'Key'" />
        <span class="kv-arrow mdi mdi-arrow-right" aria-hidden="true"></span>
        <input v-model="p.v" class="form-input" :placeholder="f.value_placeholder || ''" :aria-label="f.value_label || 'Value'" />
        <button type="button" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="removePair(i)"><span class="mdi mdi-close"></span></button>
      </div>
      <button type="button" class="btn btn-sm btn-secondary" @click="addPair"><span class="mdi mdi-plus"></span> Add {{ addNoun }}</button>
    </template>

    <!-- "source: target" mapping list (forwardAuth response headers/params) -->
    <template v-else-if="f.type === 'pairs'">
      <div v-if="maps.length" class="kv-head">
        <span>{{ f.key_label || 'From' }}</span>
        <span>{{ f.value_label || 'To' }}</span>
      </div>
      <div v-for="(m, i) in maps" :key="i" class="kv-row">
        <input v-model="m.from" class="form-input" :placeholder="f.key_placeholder || ''" :aria-label="f.key_label || 'From'" />
        <span class="kv-arrow mdi mdi-arrow-right" aria-hidden="true"></span>
        <input
          v-model="m.to"
          class="form-input"
          :placeholder="f.value_optional ? `${f.value_placeholder || ''} (same name)` : f.value_placeholder || ''"
          :aria-label="f.value_label || 'To'"
        />
        <button type="button" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="removeMapping(i)"><span class="mdi mdi-close"></span></button>
      </div>
      <button type="button" class="btn btn-sm btn-secondary" @click="addMapping"><span class="mdi mdi-plus"></span> Add {{ addNoun }}</button>
    </template>

    <!-- nested object group (cors, cookie attributes) -->
    <div v-else-if="f.type === 'object'" class="mw-group">
      <MiddlewareField v-for="sub in f.fields" :key="sub.key" :field="sub" :model="model[f.key]" :editing="editing" />
    </div>

    <!-- repeatable list of objects (errors, setCookies) -->
    <template v-else-if="f.type === 'list'">
      <div v-for="(row, i) in rows()" :key="i" class="mw-list-item">
        <div class="mw-list-head">
          <span class="mw-list-idx">#{{ i + 1 }}</span>
          <button type="button" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="removeRow(i)"><span class="mdi mdi-close"></span></button>
        </div>
        <MiddlewareField v-for="sub in f.fields" :key="sub.key" :field="sub" :model="row" :editing="editing" />
      </div>
      <button type="button" class="btn btn-sm btn-secondary" @click="addRow"><span class="mdi mdi-plus"></span> Add {{ addNoun }}</button>
    </template>

    <!-- users editor (basicAuth) -->
    <template v-else-if="f.type === 'users'">
      <div v-for="(u, i) in userRows()" :key="i" class="kv-row">
        <input v-model="u.username" class="form-input" placeholder="username" aria-label="Username" />
        <input v-model="u.password" class="form-input" type="password" :placeholder="editing ? '•••• (unchanged)' : 'password'" aria-label="Password" />
        <button type="button" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="removeUser(i)"><span class="mdi mdi-close"></span></button>
      </div>
      <button type="button" class="btn btn-sm btn-secondary" @click="addUser"><span class="mdi mdi-plus"></span> Add user</button>
    </template>

    <!-- enum -->
    <select v-else-if="f.type === 'enum'" v-model="model[f.key]" class="form-select" :aria-label="f.label">
      <option v-if="!f.required" :value="undefined">(default)</option>
      <option v-for="o in f.options" :key="o" :value="o">{{ o }}</option>
    </select>

    <!-- bool -->
    <label v-else-if="f.type === 'bool'" class="check-row">
      <input v-model="model[f.key]" type="checkbox" /> <span>{{ f.label }}</span>
      <span v-if="f.secret" class="mdi mdi-lock-outline secret-ico" title="Stored encrypted"></span>
    </label>

    <!-- int -->
    <input v-else-if="f.type === 'int'" v-model.number="model[f.key]" class="form-input" type="number" :aria-label="f.label" :placeholder="f.placeholder || ''" />

    <!-- string[] / int[] as chips -->
    <div v-else-if="f.type === 'string[]' || f.type === 'int[]'" class="tags">
      <span v-for="(t, i) in tags()" :key="i" class="tag">
        {{ t }}
        <button type="button" class="tag-x" :title="`Remove ${t}`" :aria-label="`Remove ${t}`" @click="removeTag(i)">
          <span class="mdi mdi-close"></span>
        </button>
      </span>
      <input
        v-model="draft"
        class="tag-input"
        :type="f.type === 'int[]' ? 'text' : 'text'"
        :placeholder="tags().length ? '' : f.placeholder || 'Type a value and press Enter'"
        :aria-label="f.label"
        @keydown="onTagKey"
        @paste="onTagPaste"
        @blur="commitDraft"
      />
    </div>


    <div v-else class="mw-input-row">
      <input
        v-model="model[f.key]"
        class="form-input"
        :type="f.secret ? 'password' : 'text'"
        :placeholder="f.secret && editing ? '•••• (unchanged)' : f.placeholder || ''"
        :aria-label="f.label"
      />
      <GenerateButton v-if="f.secret" :label="`Generate a value for ${f.label}`"
        @generated="model[f.key] = $event" />
    </div>

    <p v-if="f.help" class="form-hint">{{ f.help }}</p>
  </div>
</template>

<style scoped>
.mw-field { margin-bottom: 14px; }
.mw-input-row { display: flex; align-items: center; gap: 6px; }
.mw-input-row .form-input { flex: 1; min-width: 0; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.req { color: var(--danger-600); margin-left: 2px; }
.secret-ico { font-size: 13px; color: var(--text-muted); margin-left: 6px; }

.kv-head {
  display: flex; gap: 8px; margin-bottom: 4px;
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted);
}
/* Match the row's input widths so the headings sit over their columns. */
.kv-head span { flex: 1; }
.kv-head span:first-child { margin-right: 22px; }
.kv-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.kv-row .form-input { flex: 1; min-width: 0; }
.kv-arrow { color: var(--text-muted); font-size: 15px; flex: 0 0 auto; }

.check-row { display: flex; align-items: center; gap: 8px; color: var(--text-primary); }
.check-row input { width: auto; margin: 0; }
.mw-group, .mw-list-item {
  border: 1px solid var(--border-primary); border-radius: 8px;
  padding: 12px; margin-bottom: 10px; background: var(--bg-secondary);
}
.mw-list-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.mw-list-idx { font-size: 12px; color: var(--text-muted); font-weight: 600; }

/* Tag editor: the box looks like one input, the chips live inside it. */
.tags {
  display: flex; flex-wrap: wrap; align-items: center; gap: 6px;
  padding: 6px 8px; min-height: 38px;
  border: 1px solid var(--border-input); border-radius: var(--radius);
  background: var(--bg-input); cursor: text;
}
.tags:focus-within { border-color: var(--border-focus); box-shadow: var(--shadow-focus); }
.tag {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 4px 2px 9px; border-radius: 999px;
  background: var(--bg-tertiary); border: 1px solid var(--border-primary);
  color: var(--text-primary); font-size: 12px; font-family: 'JetBrains Mono', monospace;
  max-width: 100%; word-break: break-all;
}
.tag-x {
  display: inline-flex; padding: 0; border: none; background: none;
  color: var(--text-muted); cursor: pointer; font-size: 14px; line-height: 1;
}
.tag-x:hover { color: var(--danger-500); }
.tag-input {
  flex: 1; min-width: 120px; border: none; outline: none; background: transparent;
  color: var(--text-primary); font-size: 13px; padding: 2px 0;
}
.tag-input::placeholder { color: var(--text-muted); }
</style>
