<script setup lang="ts">
import { reactive, watch } from 'vue'
import type { MiddlewareField } from '@/api/types'

// Recursive renderer for one middleware rule field. It mutates model[field.key]
// in place (matching the form's direct-binding style), and recurses for nested
// object groups and list rows — so header maps, CORS blocks, cookies and the
// error-interceptor list are all editable from the form with no raw YAML.
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
  else if ((f.type === 'list' || f.type === 'users') && !Array.isArray(v)) props.model[f.key] = []
}
ensureShape()

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

// --- list editor ([]object) ---
function rows(): Array<Record<string, any>> {
  return (props.model[f.key] as Array<Record<string, any>>) || []
}
function addRow() {
  const row: Record<string, any> = {}
  for (const sub of f.fields ?? []) {
    if (sub.type === 'list') row[sub.key] = []
    else if (sub.type === 'map' || sub.type === 'object') row[sub.key] = {}
    else if (sub.default !== undefined && sub.default !== null) row[sub.key] = sub.default
  }
  ;(props.model[f.key] ||= []).push(row)
}
function removeRow(i: number) {
  rows().splice(i, 1)
}

// --- scalar helpers ---
function csvValue(): string {
  const v = props.model[f.key]
  return Array.isArray(v) ? v.join(', ') : ''
}
function onCsv(e: Event) {
  const raw = (e.target as HTMLInputElement).value.split(',').map((x) => x.trim()).filter(Boolean)
  props.model[f.key] = f.type === 'int[]' ? raw.map(Number).filter((n) => !Number.isNaN(n)) : raw
}
</script>

<template>
  <div class="mw-field">
    <label v-if="f.type !== 'bool'" class="form-label">
      {{ f.label }}<span v-if="f.required" class="req">*</span>
      <span v-if="f.secret" class="mdi mdi-lock-outline secret-ico" title="Stored encrypted"></span>
    </label>

    <!-- key/value map (setHeaders) -->
    <template v-if="f.type === 'map'">
      <div v-for="(p, i) in pairs" :key="i" class="kv-row">
        <input v-model="p.k" class="form-input" placeholder="Header-Name" aria-label="Key" />
        <input v-model="p.v" class="form-input" placeholder="value (empty removes)" aria-label="Value" />
        <button type="button" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="removePair(i)"><span class="mdi mdi-close"></span></button>
      </div>
      <button type="button" class="btn btn-sm btn-secondary" @click="addPair"><span class="mdi mdi-plus"></span> Add header</button>
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
      <button type="button" class="btn btn-sm btn-secondary" @click="addRow"><span class="mdi mdi-plus"></span> Add {{ f.label.toLowerCase() }}</button>
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
    <select v-else-if="f.type === 'enum'" v-model="model[f.key]" class="form-input" :aria-label="f.label">
      <option v-for="o in f.options" :key="o" :value="o">{{ o }}</option>
    </select>

    <!-- bool -->
    <label v-else-if="f.type === 'bool'" class="check-row">
      <input v-model="model[f.key]" type="checkbox" /> <span>{{ f.label }}</span>
      <span v-if="f.secret" class="mdi mdi-lock-outline secret-ico" title="Stored encrypted"></span>
    </label>

    <!-- int -->
    <input v-else-if="f.type === 'int'" v-model.number="model[f.key]" class="form-input" type="number" :aria-label="f.label" />

    <!-- string[] / int[] -->
    <input
      v-else-if="f.type === 'string[]' || f.type === 'int[]'"
      class="form-input"
      :value="csvValue()"
      placeholder="comma-separated"
      :aria-label="f.label"
      @input="onCsv"
    />

    <!-- string / duration / secret -->
    <input
      v-else
      v-model="model[f.key]"
      class="form-input"
      :type="f.secret ? 'password' : 'text'"
      :placeholder="f.secret && editing ? '•••• (unchanged)' : ''"
      :aria-label="f.label"
    />

    <p v-if="f.help" class="form-hint">{{ f.help }}</p>
  </div>
</template>

<style scoped>
.mw-field { margin-bottom: 14px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.req { color: var(--danger-600); margin-left: 2px; }
.secret-ico { font-size: 13px; color: var(--text-muted); margin-left: 6px; }
.kv-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.kv-row .form-input { flex: 1; }
.check-row { display: flex; align-items: center; gap: 8px; color: var(--text-primary); }
.check-row input { width: auto; margin: 0; }
.mw-group, .mw-list-item {
  border: 1px solid var(--border-primary); border-radius: 8px;
  padding: 12px; margin-bottom: 10px; background: var(--bg-secondary);
}
.mw-list-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.mw-list-idx { font-size: 12px; color: var(--text-muted); font-weight: 600; }
</style>
