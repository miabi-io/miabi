<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { appApi } from '@/api/apps'
import type {
  Application,
  CanaryMatchRule,
  CanaryMatchSource,
  CanaryMatchOperator,
  CanaryMode,
  CanaryPreview,
} from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'

const props = defineProps<{ app: Application; wsId: number; canEdit: boolean }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const notify = useNotificationStore()
// Gate the edit, not the traffic: `has` keeps an already-configured rule set
// visible and serving, `mutable` is what a change needs.
const advanced = useEntitlement('advanced_canary')

const SOURCES: { value: CanaryMatchSource; label: string }[] = [
  { value: 'header', label: 'Header' },
  { value: 'query', label: 'Query param' },
  { value: 'cookie', label: 'Cookie' },
  { value: 'ip', label: 'Client IP' },
]
const OPERATORS: { value: CanaryMatchOperator; label: string }[] = [
  { value: 'equals', label: 'equals' },
  { value: 'not_equals', label: 'does not equal' },
  { value: 'contains', label: 'contains' },
  { value: 'not_contains', label: 'does not contain' },
  { value: 'starts_with', label: 'starts with' },
  { value: 'ends_with', label: 'ends with' },
  { value: 'regex', label: 'matches regex' },
  { value: 'in', label: 'is one of (comma-separated)' },
]

const mode = ref<CanaryMode>('auto')
const exclusive = ref(false)
const priority = ref(0)
const rules = ref<CanaryMatchRule[]>([])
const saving = ref(false)
const savedWarnings = ref<string[]>([])

const canaryActive = computed(() => !!props.app.canary_release_id)
const weight = computed(() => props.app.canary_weight ?? 0)
// What the rules are judged against: the live share, or the share the next
// rollout starts at when none is running.
const projectedWeight = computed(() => (canaryActive.value ? weight.value : (props.app.canary_initial_weight ?? 10)))
const editable = computed(() => props.canEdit && advanced.mutable.value)
// A configured-but-lapsed licence: keep showing what is serving, refuse changes.
const readOnlyNotice = computed(() => advanced.has.value && !advanced.mutable.value)

// syncFromApp resets the form to what is stored, so a reload or an external
// change never leaves stale edits looking saved.
function syncFromApp() {
  mode.value = props.app.canary_mode ?? 'auto'
  exclusive.value = !!props.app.canary_exclusive
  priority.value = props.app.canary_priority ?? 0
  rules.value = (props.app.canary_match ?? []).map((r) => ({ ...r }))
}
watch(() => props.app, syncFromApp, { immediate: true, deep: true })

const dirty = computed(() => {
  const stored = {
    mode: props.app.canary_mode ?? 'auto',
    exclusive: !!props.app.canary_exclusive,
    priority: props.app.canary_priority ?? 0,
    match: props.app.canary_match ?? [],
  }
  return JSON.stringify(stored) !== JSON.stringify({ mode: mode.value, exclusive: exclusive.value, priority: priority.value, match: rules.value })
})

function addRule() {
  rules.value.push({ source: 'header', name: '', operator: 'equals', value: '' })
}
function removeRule(i: number) {
  rules.value.splice(i, 1)
}

// Switching to the automatic ramp drops the rules with it: automatic mode is
// this same model with an empty rule set, and the backend enforces that.
watch(mode, (m) => {
  if (m === 'auto') {
    rules.value = []
    exclusive.value = false
    priority.value = 0
  }
})

// Client-side echo of the backend's refusals, so a config that would route
// somewhere surprising is caught before a round trip.
const problem = computed<string | null>(() => {
  if (mode.value === 'auto') return null
  if (exclusive.value && rules.value.length === 0) {
    return 'An exclusive canary needs at least one match rule; without one it takes all traffic.'
  }
  if (!exclusive.value && rules.value.length > 0 && projectedWeight.value <= 0) {
    return 'A canary at 0% needs exclusive routing; otherwise its match rules send no traffic.'
  }
  for (const r of rules.value) {
    if (r.source !== 'ip' && !r.name.trim()) return 'Rules on a header, query or cookie need a name.'
    if (!r.value.trim()) return 'Every rule needs a value.'
    if (r.operator === 'regex') {
      try {
        new RegExp(r.value)
      } catch {
        return `“${r.value}” is not a valid regular expression.`
      }
    }
  }
  return null
})

// Advisory, not blocking: an IP rule is only as trustworthy as the gateway's
// trusted-proxy configuration. The backend says the same on save.
const ipWarning = computed(() => mode.value === 'manual' && rules.value.some((r) => r.source === 'ip'))

async function save() {
  if (problem.value) return
  saving.value = true
  try {
    const res = await appApi.setCanaryRouting(props.wsId, props.app.id, {
      mode: mode.value,
      exclusive: exclusive.value,
      priority: priority.value,
      match: rules.value,
    })
    // Warnings persist as a banner rather than a toast: they describe something
    // outside Miabi that has to be true, which outlives a 4-second notification.
    savedWarnings.value = res.data.data?.warnings ?? []
    notify.success('Canary routing updated')
    emit('changed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

// --- Weight ---------------------------------------------------------------
const weightDraft = ref(0)
const weightBusy = ref(false)
watch(
  () => props.app.canary_weight,
  (w) => {
    weightDraft.value = w ?? 0
  },
  { immediate: true },
)
// 0 only means something for an exclusive canary, where the rules alone decide
// who reaches it.
const minWeight = computed(() => (exclusive.value && rules.value.length > 0 ? 0 : 1))

async function applyWeight() {
  weightBusy.value = true
  try {
    await appApi.setCanaryWeight(props.wsId, props.app.id, weightDraft.value)
    notify.success(`Canary traffic set to ${weightDraft.value}%`)
    emit('changed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    weightBusy.value = false
  }
}

// --- Preview --------------------------------------------------------------
const preview = ref<CanaryPreview | null>(null)
const previewBusy = ref(false)
const probe = ref({ headerName: '', headerValue: '', queryName: '', queryValue: '', cookieName: '', cookieValue: '', ip: '' })

// Seed the probe fields from the saved rules, so the first preview is one click
// rather than retyping what the rules already name.
function seedProbe() {
  for (const r of props.app.canary_match ?? []) {
    if (r.source === 'header' && !probe.value.headerName) probe.value.headerName = r.name
    if (r.source === 'query' && !probe.value.queryName) probe.value.queryName = r.name
    if (r.source === 'cookie' && !probe.value.cookieName) probe.value.cookieName = r.name
  }
}
watch(() => props.app.canary_match, seedProbe, { immediate: true })

async function runPreview() {
  previewBusy.value = true
  try {
    const p = probe.value
    preview.value = (
      await appApi.previewCanaryRouting(props.wsId, props.app.id, {
        headers: p.headerName ? { [p.headerName]: p.headerValue } : {},
        query: p.queryName ? { [p.queryName]: p.queryValue } : {},
        cookies: p.cookieName ? { [p.cookieName]: p.cookieValue } : {},
        ip: p.ip,
      })
    ).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    previewBusy.value = false
  }
}

const previewLabel = computed(() => {
  if (!preview.value) return ''
  if (preview.value.backend === 'canary') return 'Canary'
  if (preview.value.backend === 'stable') return 'Stable'
  return `Split — ${preview.value.canary_chance}% canary`
})
</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>Canary routing</h2>
      <span v-if="!advanced.has.value" class="badge badge-muted">Enterprise</span>
    </div>

    <!-- Community / unlicensed: the automatic ramp still works, this is the upsell. -->
    <div v-if="!advanced.has.value" class="card-body locked">
      <span class="mdi mdi-lock-outline"></span>
      <div>
        <p>
          Hold the canary at a weight you choose instead of watching an automatic ramp, and send only the requests you
          pick — a header, a cookie, a query parameter, an IP — to the new release.
        </p>
        <router-link to="/admin/license" class="btn btn-secondary btn-sm">Upgrade</router-link>
      </div>
    </div>

    <div v-else class="card-body">
      <!-- A lapsed licence must never move production traffic: what is configured
           keeps serving, and only changes are refused. -->
      <div v-if="readOnlyNotice" class="banner banner-warning">
        <span class="mdi mdi-lock-clock"></span>
        <span>
          This canary keeps routing exactly as configured, but the licence has expired, so the rules are read-only.
          <router-link to="/admin/license">Renew</router-link> to change them.
        </span>
      </div>

      <!-- Mode -->
      <div class="field">
        <label class="form-label">Mode</label>
        <div class="mode-choices">
          <label class="mode-choice" :class="{ active: mode === 'auto' }">
            <input v-model="mode" type="radio" value="auto" :disabled="!editable" />
            <div>
              <span class="mode-name">Automatic ramp</span>
              <span class="mode-hint">The platform raises the weight on a timer and promotes at 100%.</span>
            </div>
          </label>
          <label class="mode-choice" :class="{ active: mode === 'manual' }">
            <input v-model="mode" type="radio" value="manual" :disabled="!editable" />
            <div>
              <span class="mode-name">Manual</span>
              <span class="mode-hint">The weight stays where you put it, and match rules can steer who reaches the canary.</span>
            </div>
          </label>
        </div>
      </div>

      <!-- Weight: honest about which mode it is in. -->
      <div class="field">
        <label class="form-label">
          Traffic to canary
          <span class="text-muted">
            · {{ mode === 'manual' ? 'the only thing that moves traffic' : 'where the ramp has reached' }}
          </span>
        </label>
        <div v-if="canaryActive" class="weight-row">
          <input
            v-model.number="weightDraft"
            type="range"
            :min="minWeight"
            max="99"
            :disabled="!canEdit || mode !== 'manual'"
            class="weight-slider"
          />
          <input
            v-model.number="weightDraft"
            type="number"
            :min="minWeight"
            max="99"
            :disabled="!canEdit || mode !== 'manual'"
            class="form-input weight-number"
          />
          <span class="text-muted">%</span>
          <button
            v-if="mode === 'manual'"
            class="btn btn-secondary btn-sm"
            :disabled="!canEdit || weightBusy || weightDraft === weight"
            @click="applyWeight"
          >
            Apply
          </button>
        </div>
        <p v-else class="hint">
          No canary is running. Rules saved now apply to the next rollout, which starts at {{ projectedWeight }}%.
        </p>
      </div>

      <!-- Rule builder -->
      <template v-if="mode === 'manual'">
        <div class="field">
          <label class="form-label">Match rules</label>
          <p class="hint">
            A request reaches the canary only when <strong>every</strong> rule holds. With no rules, the weight alone
            decides.
          </p>
          <div v-if="rules.length" class="kv-head">
            <span>Source</span><span>Name</span><span>Operator</span><span>Value</span>
          </div>
          <div v-for="(r, i) in rules" :key="i" class="rule-row">
            <select v-model="r.source" class="form-input" :disabled="!editable" aria-label="Source">
              <option v-for="s in SOURCES" :key="s.value" :value="s.value">{{ s.label }}</option>
            </select>
            <input
              v-model="r.name"
              class="form-input"
              :disabled="!editable || r.source === 'ip'"
              :placeholder="r.source === 'ip' ? '—' : 'X-Canary-User'"
              aria-label="Name"
            />
            <select v-model="r.operator" class="form-input" :disabled="!editable" aria-label="Operator">
              <option v-for="o in OPERATORS" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
            <input v-model="r.value" class="form-input" :disabled="!editable" placeholder="true" aria-label="Value" />
            <button
              type="button"
              class="btn-icon btn-icon-danger"
              title="Remove"
              aria-label="Remove"
              :disabled="!editable"
              @click="removeRule(i)"
            >
              <span class="mdi mdi-close"></span>
            </button>
          </div>
          <button type="button" class="btn btn-sm btn-secondary" :disabled="!editable" @click="addRule">
            <span class="mdi mdi-plus"></span> Add rule
          </button>
        </div>

        <div class="field">
          <label class="check-row">
            <input v-model="exclusive" type="checkbox" :disabled="!editable" />
            <span>Exclusive — matching requests go entirely to the canary, ignoring the weight</span>
          </label>
          <p class="hint">
            Off, a matching request instead joins the weighted pool and reaches the canary
            {{ projectedWeight }}% of the time.
          </p>
        </div>

        <div v-if="exclusive" class="field">
          <label class="form-label">Priority</label>
          <input v-model.number="priority" type="number" min="0" max="1000" class="form-input priority-input" :disabled="!editable" />
          <p class="hint">Breaks ties when several exclusive backends match. One canary per app today, so this is reserved.</p>
        </div>

        <div v-if="ipWarning" class="banner banner-warning">
          <span class="mdi mdi-alert-outline"></span>
          <span>
            This rule set routes on client IP. Unless the gateway has <code>proxy.trustedProxies</code> configured, the
            client address is whatever the caller claims — anyone could put themselves in the canary.
            <a href="https://goma.jkaninda.dev/usermanual/running-behind-a-proxy.html" target="_blank" rel="noopener">
              Configuring trusted proxies
            </a>
          </span>
        </div>
      </template>

      <div v-for="(w, i) in savedWarnings" :key="i" class="banner banner-warning">
        <span class="mdi mdi-alert-outline"></span>
        <span>{{ w }}</span>
      </div>

      <div v-if="problem" class="banner banner-error">
        <span class="mdi mdi-alert-circle-outline"></span>
        <span>{{ problem }}</span>
      </div>

      <div class="actions">
        <button class="btn btn-primary btn-sm" :disabled="!editable || saving || !dirty || !!problem" @click="save">
          Save routing
        </button>
        <button class="btn btn-secondary btn-sm" :disabled="!dirty || saving" @click="syncFromApp">Reset</button>
      </div>

      <!-- Which backend would serve this? -->
      <div class="field preview">
        <label class="form-label">Which backend would serve this?</label>
        <p class="hint">
          Resolves a request against the <strong>saved</strong> rules. Nothing is deployed and no traffic moves — save
          your changes first to preview them.
        </p>
        <div class="probe-grid">
          <div class="probe-pair">
            <input v-model="probe.headerName" class="form-input" placeholder="Header name" aria-label="Header name" />
            <input v-model="probe.headerValue" class="form-input" placeholder="Header value" aria-label="Header value" />
          </div>
          <div class="probe-pair">
            <input v-model="probe.queryName" class="form-input" placeholder="Query param" aria-label="Query parameter" />
            <input v-model="probe.queryValue" class="form-input" placeholder="Query value" aria-label="Query value" />
          </div>
          <div class="probe-pair">
            <input v-model="probe.cookieName" class="form-input" placeholder="Cookie name" aria-label="Cookie name" />
            <input v-model="probe.cookieValue" class="form-input" placeholder="Cookie value" aria-label="Cookie value" />
          </div>
          <div class="probe-pair">
            <input v-model="probe.ip" class="form-input" placeholder="Client IP" aria-label="Client IP" />
          </div>
        </div>
        <button class="btn btn-secondary btn-sm" :disabled="previewBusy" @click="runPreview">
          <span class="mdi mdi-play-outline"></span> Preview
        </button>

        <div v-if="preview" class="preview-result" :class="`preview-${preview.backend}`">
          <div class="preview-verdict">
            <span class="mdi" :class="preview.backend === 'stable' ? 'mdi-server' : 'mdi-call-split'"></span>
            <strong>{{ previewLabel }}</strong>
          </div>
          <p class="preview-reason">{{ preview.reason }}</p>
          <ul v-if="preview.rules?.length" class="preview-rules">
            <li v-for="(r, i) in preview.rules" :key="i" :class="{ hit: r.matched }">
              <span class="mdi" :class="r.matched ? 'mdi-check' : 'mdi-close'"></span>
              <code>{{ r.rule.source }}{{ r.rule.name ? ' ' + r.rule.name : '' }} {{ r.rule.operator }} {{ r.rule.value }}</code>
              <span class="text-muted">read “{{ r.actual }}”</span>
            </li>
          </ul>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.card-header { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.locked { display: flex; align-items: center; gap: 14px; }
.locked .mdi { font-size: 28px; color: var(--text-muted); }
.locked p { margin: 0 0 8px; color: var(--text-secondary, var(--text-muted)); max-width: 60ch; }

.field { margin-bottom: 18px; }
.hint { margin: 4px 0 10px; font-size: 12px; color: var(--text-muted); max-width: 70ch; }

.mode-choices { display: flex; gap: 10px; flex-wrap: wrap; }
.mode-choice {
  display: flex; align-items: flex-start; gap: 9px; flex: 1; min-width: 260px;
  padding: 10px 12px; border: 1px solid var(--border-primary); border-radius: 8px; cursor: pointer;
}
.mode-choice.active { border-color: var(--primary-500, #6366f1); background: var(--bg-secondary); }
.mode-choice input { width: auto; margin: 3px 0 0; }
.mode-name { display: block; font-size: 13px; font-weight: 600; color: var(--text-primary); }
.mode-hint { display: block; margin-top: 2px; font-size: 12px; color: var(--text-muted); }

.weight-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.weight-slider { flex: 1; min-width: 180px; }
.weight-number { width: 84px; flex: 0 0 auto; }
.priority-input { width: 120px; }

/* Four columns + the remove button, matching the middleware form's pair editors. */
.kv-head {
  display: grid; grid-template-columns: 1.1fr 1.3fr 1.4fr 1.4fr 32px; gap: 8px; margin-bottom: 4px;
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted);
}
.rule-row { display: grid; grid-template-columns: 1.1fr 1.3fr 1.4fr 1.4fr 32px; gap: 8px; align-items: center; margin-bottom: 8px; }
.rule-row .form-input { min-width: 0; }
.check-row { display: flex; align-items: center; gap: 8px; color: var(--text-primary); }
.check-row input { width: auto; margin: 0; }

.banner { display: flex; align-items: flex-start; gap: 9px; padding: 10px 12px; border-radius: 8px; font-size: 12.5px; margin-bottom: 14px; }
.banner .mdi { font-size: 17px; flex: 0 0 auto; }
.banner-warning { border: 1px solid var(--warning-500, #f59e0b); color: var(--text-primary); }
.banner-error { border: 1px solid var(--danger-500, #ef4444); color: var(--text-primary); }
.banner code { font-size: 12px; }

.actions { display: flex; gap: 8px; margin-bottom: 22px; }

.preview { border-top: 1px solid var(--border-primary); padding-top: 16px; }
.probe-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 8px; margin-bottom: 10px; }
.probe-pair { display: flex; gap: 8px; }
.probe-pair .form-input { flex: 1; min-width: 0; }

.preview-result { margin-top: 12px; padding: 12px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); }
.preview-canary { border-color: var(--warning-500, #f59e0b); }
.preview-stable { border-color: var(--success-500, #22c55e); }
.preview-verdict { display: flex; align-items: center; gap: 7px; font-size: 14px; color: var(--text-primary); }
.preview-reason { margin: 6px 0 0; font-size: 12.5px; color: var(--text-secondary, var(--text-muted)); }
.preview-rules { list-style: none; margin: 10px 0 0; padding: 0; display: flex; flex-direction: column; gap: 5px; }
.preview-rules li { display: flex; align-items: center; gap: 7px; font-size: 12px; color: var(--text-muted); }
.preview-rules li.hit { color: var(--text-primary); }
.preview-rules .mdi-check { color: var(--success-500, #22c55e); }
.preview-rules .mdi-close { color: var(--danger-500, #ef4444); }
</style>
