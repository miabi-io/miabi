<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { middlewareApi } from '@/api/middlewares'
import { useNotificationStore } from '@/stores/notification'
import { parseYaml, toYaml } from '@/utils/yaml'
import MiddlewareField from '@/components/MiddlewareField.vue'
import type { Middleware, MiddlewareCatalog, MiddlewareDescriptor, MiddlewareField as MwField, MiddlewarePreset } from '@/api/types'

// Shared create/edit modal for Goma middlewares, driven by the server catalog:
// a curated type picker + schema form for known types, with a raw-YAML "advanced"
// fallback for uncatalogued types. Secret fields are masked and round-trip a
// redaction sentinel so editing a policy never wipes its stored password.
const props = defineProps<{ open: boolean; workspaceId: number | null; editing: Middleware | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved', m: Middleware): void }>()

const notify = useNotificationStore()

// Catalog is static across workspaces; fetch once and cache for the session.
let cached: MiddlewareCatalog | null = null
const catalog = ref<MiddlewareCatalog | null>(cached)

const form = ref({ name: '', type: 'basicAuth', paths: '' })
const rule = ref<Record<string, any>>({})
const ruleText = ref('') // advanced / uncatalogued raw YAML
const advanced = ref(false)
const ruleError = ref('')
const saving = ref(false)

const descriptors = computed(() => catalog.value?.types ?? [])
const presets = computed(() => catalog.value?.presets ?? [])
const descriptor = computed<MiddlewareDescriptor | undefined>(() => descriptors.value.find((d) => d.type === form.value.type))
// Fields rendered in the schema form. Every type has a form editor now, except a
// free-form object with no sub-schema (rare: jwt forwardHeaders, rateLimit
// keyStrategy) — those still fall to the advanced YAML box.
const isBareObject = (f: MwField) => f.type === 'object' && !(f.fields && f.fields.length)
const fields = computed<MwField[]>(() => (descriptor.value?.fields ?? []).filter((f) => !isBareObject(f)))
// True when the type has any field the form can't render (forces advanced YAML).
const hasBareObject = computed(() => (descriptor.value?.fields ?? []).some(isBareObject))
const isCatalogued = computed(() => !!descriptor.value)
const showAdvanced = computed(() => advanced.value || !isCatalogued.value)

const categories = ['access', 'security', 'traffic', 'transform', 'observability']
const typesByCategory = computed(() =>
  categories
    .map((cat) => ({ cat, items: descriptors.value.filter((d) => d.category === cat) }))
    .filter((g) => g.items.length),
)

async function ensureCatalog() {
  if (catalog.value || !props.workspaceId) return
  try {
    cached = (await middlewareApi.catalog(props.workspaceId)).data.data
    catalog.value = cached
  } catch {
    /* form still works in advanced mode */
  }
}

function defaultsFor(d?: MiddlewareDescriptor): Record<string, any> {
  const r: Record<string, any> = {}
  for (const f of d?.fields ?? []) {
    if (f.type === 'users' || f.type === 'list') r[f.key] = []
    else if (f.type === 'map' || f.type === 'object') r[f.key] = {}
    else if (f.default !== undefined && f.default !== null) r[f.key] = f.default
  }
  return r
}

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    ruleError.value = ''
    advanced.value = false
    await ensureCatalog()
    if (props.editing) {
      const m = props.editing
      form.value = { name: m.name, type: m.type, paths: (m.paths || []).join(', ') }
      rule.value = JSON.parse(JSON.stringify(m.rule || {}))
      ruleText.value = toYaml(m.rule || {})
    } else {
      const first = descriptors.value[0]?.type || 'basicAuth'
      form.value = { name: '', type: first, paths: '' }
      rule.value = defaultsFor(descriptors.value[0])
      ruleText.value = ''
    }
  },
)

// Switching type (in create mode) resets the rule to the new type's defaults.
function onTypeChange() {
  if (props.editing) return
  rule.value = defaultsFor(descriptor.value)
}

function applyPreset(p: MiddlewarePreset) {
  form.value.type = p.type
  rule.value = { ...defaultsFor(descriptors.value.find((d) => d.type === p.type)), ...JSON.parse(JSON.stringify(p.rule)) }
  advanced.value = false
}

function splitCsv(s: string): string[] {
  return s.split(',').map((x) => x.trim()).filter(Boolean)
}

// isEmpty reports whether a built value should be dropped from a non-required
// field: blank string, empty list, or an object/map with no keys.
function isEmpty(v: unknown): boolean {
  if (v === undefined || v === null) return true
  if (typeof v === 'string') return v.trim() === ''
  if (Array.isArray(v)) return v.length === 0
  if (typeof v === 'object') return Object.keys(v as object).length === 0
  return false
}

// buildRule produces the rule to submit. In form mode the fields already hold the
// correct shapes (MiddlewareField coerces ints, lists, maps and nested groups as
// it edits), so this only prunes empty non-required top-level fields and drops
// blank basicAuth users. Advanced mode parses the raw YAML box verbatim.
function buildRule(): Record<string, unknown> {
  if (showAdvanced.value) return ruleText.value.trim() ? parseYaml(ruleText.value) : {}
  const out: Record<string, any> = JSON.parse(JSON.stringify(rule.value))
  for (const f of descriptor.value?.fields ?? []) {
    if (f.type === 'users') out[f.key] = (Array.isArray(out[f.key]) ? out[f.key] : []).filter((u: any) => u && u.username)
    if (!f.required && isEmpty(out[f.key])) delete out[f.key]
  }
  return out
}

async function save() {
  if (!props.workspaceId) return
  let builtRule: Record<string, unknown>
  try {
    builtRule = buildRule()
  } catch {
    ruleError.value = 'Rule must be valid YAML'
    return
  }
  ruleError.value = ''
  saving.value = true
  try {
    const input = { name: form.value.name, type: form.value.type, paths: splitCsv(form.value.paths), rule: builtRule }
    const res = props.editing
      ? await middlewareApi.update(props.workspaceId, props.editing.id, input)
      : await middlewareApi.create(props.workspaceId, input)
    notify.success(props.editing ? 'Policy updated' : 'Policy created')
    emit('saved', res.data.data)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="emit('close')">
      <div class="modal modal-lg">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit policy' : 'New security policy' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="emit('close')"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <!-- One-click presets (create only) -->
            <div v-if="!editing && presets.length" class="presets">
              <button
                v-for="p in presets"
                :key="p.key"
                type="button"
                class="preset-chip"
                :title="p.description"
                @click="applyPreset(p)"
              >
                <span class="mdi mdi-flash-outline"></span> {{ p.display_name }}
              </button>
            </div>

            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. basic-auth" pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" title="Lowercase letters, digits and hyphens" aria-label="Name" required autofocus />
              <p class="form-hint">Lowercase letters, digits and hyphens (e.g. basic-auth).</p>
            </div>

            <div class="form-group">
              <label class="form-label">Type</label>
              <select v-model="form.type" class="form-input" :disabled="!!editing" aria-label="Type" @change="onTypeChange">
                <optgroup v-for="g in typesByCategory" :key="g.cat" :label="g.cat">
                  <option v-for="d in g.items" :key="d.type" :value="d.type">{{ d.display_name }}</option>
                </optgroup>
                <option v-if="!isCatalogued" :value="form.type">{{ form.type }} (advanced)</option>
              </select>
              <p v-if="descriptor" class="form-hint">{{ descriptor.description }}</p>
            </div>

            <div class="form-group">
              <label class="form-label">Paths <span class="text-muted">(comma-separated, default /*)</span></label>
              <input v-model="form.paths" class="form-input" placeholder="/*" aria-label="Paths" />
            </div>

            <!-- Schema-driven fields (recursive: scalars, maps, groups, lists) -->
            <template v-if="!showAdvanced">
              <MiddlewareField v-for="f in fields" :key="f.key" :field="f" :model="rule" :editing="!!editing" />

              <p v-if="hasBareObject" class="form-hint">
                This type has an advanced field edited as raw YAML.
              </p>
              <div v-if="isCatalogued" class="advanced-toggle">
                <button type="button" class="btn btn-sm btn-link" @click="advanced = true">
                  <span class="mdi mdi-code-braces"></span> Edit as raw YAML
                </button>
              </div>
            </template>

            <!-- Advanced raw YAML (uncatalogued type or opted in) -->
            <div v-else class="form-group" style="margin-bottom: 0">
              <div class="rule-head">
                <label class="form-label" style="margin: 0">Rule <span class="text-muted">(YAML)</span></label>
                <button v-if="isCatalogued" type="button" class="btn btn-sm btn-link" @click="advanced = false">
                  <span class="mdi mdi-form-select"></span> Back to form
                </button>
              </div>
              <textarea v-model="ruleText" class="form-textarea mono" rows="8" spellcheck="false" placeholder="requestsPerUnit: 100&#10;unit: minute" aria-label="Rule (YAML)"></textarea>
              <p v-if="ruleError" class="form-error">{{ ruleError }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="emit('close')">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Create') }}</button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.text-muted { color: var(--text-muted); font-weight: 400; }
.mono { font-family: monospace; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.rule-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px; }
.req { color: var(--danger-600); margin-left: 2px; }
.secret-ico { font-size: 13px; color: var(--text-muted); margin-left: 6px; }
.presets { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 18px; }
.preset-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 12px; border-radius: 999px;
  border: 1px solid var(--border-primary); background: var(--bg-secondary);
  color: var(--text-primary); font-size: 13px; cursor: pointer;
}
.preset-chip:hover { border-color: var(--primary-400); color: var(--primary-600); }
.preset-chip .mdi { font-size: 14px; color: var(--primary-500); }
.user-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.user-row .form-input { flex: 1; }
.check-row { display: flex; align-items: center; gap: 8px; color: var(--text-primary); }
.check-row input { width: auto; margin: 0; }
.advanced-toggle { margin-top: 4px; }
.btn-link { background: none; border: none; color: var(--primary-600); padding: 0; cursor: pointer; }
</style>
