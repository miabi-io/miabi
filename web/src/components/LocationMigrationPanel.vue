<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Live location migration (Enterprise): move this app, with its volumes and databases, to another location.
// The wizard only ever shows what the server planned; the server plans again when the move starts, so a
// stale page cannot start a move a new blocker would stop.
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { locationApi, type Location } from '@/api/locations'
import { databaseApi } from '@/api/resources'
import type { DatabaseInstance } from '@/api/types'
import {
  migrationApi,
  isOpen,
  type DBChoice,
  type DBStrategy,
  type Migration,
  type MigrationPlan,
} from '@/api/migrations'
import { useEntitlement } from '@/composables/useEntitlement'
import { useNotificationStore } from '@/stores/notification'
import { fmtSize } from '@/utils/format'
import { relativeTime } from '@/utils/time'
import { locationLabel } from '@/utils/locations'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const props = defineProps<{ wsId: number; appId: number; appName: string; clusterId?: number; canManage: boolean }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const notify = useNotificationStore()
const entitlement = useEntitlement('live_migration')

const locations = ref<Location[]>([])
const migrations = ref<Migration[]>([])
const current = computed(() => migrations.value.find(isOpen) ?? null)
const history = computed(() => migrations.value.filter((m) => !isOpen(m)).slice(0, 5))
const hereLabel = computed(() => {
  const l = locations.value.find((x) => x.id === props.clusterId)
  return l ? locationLabel(l) : ''
})
const targets = computed(() => locations.value.filter((l) => l.id !== props.clusterId))

async function load() {
  try {
    locations.value = (await locationApi.list(props.wsId)).data.data?.locations ?? []
  } catch {
    locations.value = []
  }
  if (!props.canManage) return
  try {
    migrations.value = (await migrationApi.listForApp(props.wsId, props.appId)).data.data ?? []
  } catch {
    migrations.value = []
  }
}

// --- Live progress ---
let es: EventSource | null = null
function follow(m: Migration | null) {
  es?.close()
  es = null
  if (!m || !isOpen(m)) return
  es = new EventSource(migrationApi.eventsUrl(props.wsId, m.id))
  es.onmessage = (ev) => {
    try {
      const next = (JSON.parse(ev.data) as { data?: Migration }).data
      if (!next) return
      const i = migrations.value.findIndex((x) => x.id === next.id)
      const wasOpen = i >= 0 && isOpen(migrations.value[i])
      if (i >= 0) migrations.value[i] = next
      else migrations.value.unshift(next)
      if (wasOpen && !isOpen(next)) emit('changed')
      if (next.status === 'cut_over' && next.phase === 'done') emit('changed')
    } catch { /* keep-alive */ }
  }
  es.onerror = () => {
    es?.close()
    es = null
    // The stream drops when the server restarts; reload once and follow again.
    setTimeout(async () => {
      await load()
      follow(current.value)
    }, 4000)
  }
}
watch(() => current.value?.id, () => follow(current.value))
watch(() => [props.wsId, props.appId], load, { immediate: true })
onUnmounted(() => es?.close())

const PHASES = ['prepare', 'presync', 'stop', 'final_sync', 'switch', 'verify', 'reroute']
function phaseState(m: Migration, p: string): 'done' | 'active' | 'todo' {
  if (m.status === 'cut_over') return 'done'
  const at = PHASES.indexOf(m.phase)
  const i = PHASES.indexOf(p)
  if (at < 0) return 'todo'
  if (i < at) return 'done'
  return i === at ? 'active' : 'todo'
}
const downtimeLabel = computed(() => {
  const s = current.value?.progress.estimated_downtime_seconds ?? 0
  if (!s) return ''
  return s < 90 ? t('migration.seconds', { n: s }) : t('migration.minutes', { n: Math.ceil(s / 60) })
})

// --- Wizard ---
const wizardOpen = ref(false)
const target = ref('')
const plan = ref<MigrationPlan | null>(null)
const planning = ref(false)
const choices = ref<Record<number, { strategy: DBStrategy; target?: number }>>({})
const cutoverMode = ref<'auto' | 'manual'>('auto')
const bandwidthMB = ref<number | null>(null)
const confirmName = ref('')
const starting = ref(false)
const instances = ref<DatabaseInstance[]>([])

function openWizard() {
  target.value = targets.value[0]?.name ?? ''
  plan.value = null
  choices.value = {}
  cutoverMode.value = 'auto'
  bandwidthMB.value = null
  confirmName.value = ''
  wizardOpen.value = true
  if (target.value) runPlan()
}

function dbChoices(): DBChoice[] {
  return Object.entries(choices.value).map(([id, c]) => ({
    instance_id: Number(id),
    strategy: c.strategy,
    target_instance_id: c.strategy === 'existing_instance' ? c.target : undefined,
  }))
}

async function runPlan() {
  if (!target.value) return
  planning.value = true
  try {
    plan.value = (await migrationApi.plan(props.wsId, props.appId, { location: target.value, databases: dbChoices() })).data.data
    for (const d of plan.value.databases) {
      if (!choices.value[d.instance_id] && d.strategy) {
        choices.value[d.instance_id] = { strategy: d.strategy as DBStrategy, target: d.target_instance_id }
      }
    }
    if (plan.value.databases.some((d) => d.strategies.includes('existing_instance')) && !instances.value.length) {
      instances.value = (await databaseApi.list(props.wsId)).data.data ?? []
    }
  } catch (e) {
    plan.value = null
    notify.apiError(e)
  } finally {
    planning.value = false
  }
}

function candidates(engine: string) {
  const loc = locations.value.find((l) => l.name === target.value)
  return instances.value.filter((i) => i.engine === engine && (!loc || i.cluster_id === loc.id) && i.status === 'running')
}

const canStart = computed(
  () => !!plan.value && plan.value.blockers.length === 0 && confirmName.value.trim() === props.appName && !planning.value,
)

async function start() {
  if (!canStart.value) return
  starting.value = true
  try {
    const m = (
      await migrationApi.start(props.wsId, props.appId, {
        location: target.value,
        databases: dbChoices(),
        cutover_mode: cutoverMode.value,
        bandwidth_kbps: bandwidthMB.value ? Math.round(bandwidthMB.value * 1024) : 0,
      })
    ).data.data
    migrations.value.unshift(m)
    wizardOpen.value = false
    notify.success(t('migration.started'))
  } catch (e) {
    notify.apiError(e)
    await runPlan()
  } finally {
    starting.value = false
  }
}

// --- Controls ---
type Action = 'cutover' | 'cancel' | 'rollback' | 'finalize'
const confirming = ref<Action | null>(null)
const acting = ref(false)
async function act() {
  const m = current.value
  const action = confirming.value
  if (!m || !action) return
  acting.value = true
  try {
    const next = (await migrationApi[action](props.wsId, m.id)).data.data
    const i = migrations.value.findIndex((x) => x.id === next.id)
    if (i >= 0) migrations.value[i] = next
    confirming.value = null
  } catch (e) {
    notify.apiError(e)
  } finally {
    acting.value = false
  }
}
const confirmCopy = computed(() => {
  switch (confirming.value) {
    case 'cutover':
      return { title: t('migration.cutoverTitle'), message: t('migration.cutoverMessage'), variant: 'primary' as const }
    case 'cancel':
      return { title: t('migration.cancelTitle'), message: t('migration.cancelMessage'), variant: 'danger' as const }
    case 'rollback':
      return { title: t('migration.rollbackTitle'), message: t('migration.rollbackMessage'), variant: 'danger' as const }
    case 'finalize':
      return { title: t('migration.finalizeTitle'), message: t('migration.finalizeMessage'), variant: 'danger' as const }
  }
  return { title: '', message: '', variant: 'primary' as const }
})

function statusClass(s: string) {
  switch (s) {
    case 'cut_over':
    case 'finalized':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    case 'rolled_back':
    case 'cancelled':
      return 'badge-neutral'
    default:
      return 'badge-info'
  }
}
function itemPercent(it: { bytes: number; total?: number }) {
  if (!it.total) return 0
  return Math.min(100, Math.round((it.bytes / it.total) * 100))
}
</script>

<template>
  <div v-if="locations.length > 1 || migrations.length" class="card mb-4">
    <div class="card-header">
      <div>
        <h2>{{ $t('migration.title') }}</h2>
        <p class="card-subtitle">
          <template v-if="hereLabel">{{ $t('migration.runsIn', { location: hereLabel }) }} · </template>{{ $t('migration.subtitle') }}
        </p>
      </div>
      <button
        v-if="canManage && !current"
        class="btn btn-secondary btn-sm"
        :disabled="!entitlement.has.value || !targets.length"
        :title="!entitlement.has.value ? $t('migration.enterprise') : !targets.length ? $t('migration.noTargets') : ''"
        @click="openWizard"
      >
        <span class="mdi mdi-truck-fast-outline"></span>{{ $t('migration.move') }}
      </button>
    </div>
    <div class="card-body">
      <p v-if="!canManage" class="text-muted text-sm">{{ $t('migration.needsAdmin') }}</p>
      <p v-else-if="!entitlement.has.value" class="text-muted text-sm">
        <span class="badge badge-neutral">{{ $t('migration.enterpriseBadge') }}</span> {{ $t('migration.enterprise') }}
      </p>

      <div v-if="current" class="migration">
        <div class="migration-head">
          <span class="badge" :class="statusClass(current.status)">{{ $t(`migration.status.${current.status}`) }}</span>
          <strong>{{ $t('migration.toLocation', { location: current.plan.location_label || current.plan.location }) }}</strong>
          <span v-if="current.progress.message" class="text-muted text-sm">{{ current.progress.message }}</span>
        </div>

        <ol v-if="current.status !== 'cut_over'" class="phases">
          <li v-for="p in PHASES" :key="p" :class="phaseState(current, p)">{{ $t(`migration.phase.${p}`) }}</li>
        </ol>

        <div v-if="current.progress.items?.length" class="items">
          <div v-for="it in current.progress.items" :key="`${it.kind}-${it.name}`" class="item">
            <span class="mdi" :class="it.kind === 'volume' ? 'mdi-harddisk' : 'mdi-database-outline'"></span>
            <span class="mono">{{ it.name }}</span>
            <span class="badge badge-neutral">{{ $t(`migration.item.${it.status}`) }}</span>
            <span class="text-muted text-sm">
              {{ fmtSize(it.bytes) }}<template v-if="it.total"> / {{ fmtSize(it.total) }}</template>
              <template v-if="it.passes"> · {{ $t('migration.passes', { n: it.passes }) }}</template>
            </span>
            <div v-if="it.total && it.status === 'copying'" class="bar"><div :style="{ width: itemPercent(it) + '%' }"></div></div>
            <span v-if="it.detail && it.status === 'failed'" class="text-sm danger-text">{{ it.detail }}</span>
          </div>
        </div>

        <p v-if="downtimeLabel && current.status !== 'cut_over'" class="text-sm">
          <span class="mdi mdi-timer-outline"></span> {{ $t('migration.estimatedDowntime', { time: downtimeLabel }) }}
        </p>

        <div v-if="current.status === 'cut_over'" class="grace">
          <span class="mdi mdi-backup-restore"></span>
          <div>
            <p>{{ $t('migration.graceBody', { when: current.finalize_after ? relativeTime(current.finalize_after) : '' }) }}</p>
            <ul v-if="current.report?.notes?.length" class="notes">
              <li v-for="(n, i) in current.report.notes" :key="i">{{ n }}</li>
            </ul>
          </div>
        </div>

        <div class="actions">
          <button v-if="current.status === 'awaiting_cutover'" class="btn btn-primary btn-sm" @click="confirming = 'cutover'">
            {{ $t('migration.cutover') }}
          </button>
          <button
            v-if="(current.status === 'running' && ['prepare', 'presync'].includes(current.phase) && !current.cancel_requested) || current.status === 'awaiting_cutover'"
            class="btn btn-secondary btn-sm"
            @click="confirming = 'cancel'"
          >
            {{ $t('action.cancel') }}
          </button>
          <template v-if="current.status === 'cut_over'">
            <button class="btn btn-secondary btn-sm" @click="confirming = 'rollback'">{{ $t('migration.rollback') }}</button>
            <button class="btn btn-danger btn-sm" @click="confirming = 'finalize'">{{ $t('migration.finalize') }}</button>
          </template>
        </div>
      </div>

      <div v-if="history.length" class="history">
        <h3 class="text-sm">{{ $t('migration.history') }}</h3>
        <div v-for="m in history" :key="m.id" class="history-row">
          <span class="badge" :class="statusClass(m.status)">{{ $t(`migration.status.${m.status}`) }}</span>
          <span>{{ m.plan.location_label || m.plan.location }}</span>
          <span class="text-muted text-sm">{{ relativeTime(m.created_at) }}</span>
          <span v-if="m.error" class="text-sm danger-text">{{ m.error }}</span>
        </div>
      </div>
    </div>
  </div>

  <Teleport to="body">
    <AppModal v-if="wizardOpen" max-width="760px" :escapable="!starting" @close="wizardOpen = false">
      <div class="modal-header">
        <h3>{{ $t('migration.wizardTitle', { app: appName }) }}</h3>
        <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="wizardOpen = false">
          <span class="mdi mdi-close"></span>
        </button>
      </div>
      <div class="modal-body">
        <div class="form-group">
          <label class="form-label">{{ $t('migration.targetLocation') }}</label>
          <select v-model="target" class="form-select" @change="(choices = {}), runPlan()">
            <option v-for="l in targets" :key="l.id" :value="l.name">{{ locationLabel(l) }}</option>
          </select>
        </div>

        <p v-if="planning" class="text-muted text-sm"><span class="mdi mdi-loading mdi-spin"></span> {{ $t('migration.planning') }}</p>

        <template v-if="plan && !planning">
          <div v-if="plan.blockers.length" class="issues blockers">
            <strong><span class="mdi mdi-cancel"></span> {{ $t('migration.blockers') }}</strong>
            <ul><li v-for="(b, i) in plan.blockers" :key="i">{{ b.message }}</li></ul>
          </div>
          <div v-if="plan.warnings.length" class="issues warnings">
            <strong><span class="mdi mdi-alert-outline"></span> {{ $t('migration.warnings') }}</strong>
            <ul><li v-for="(w, i) in plan.warnings" :key="i">{{ w.message }}</li></ul>
          </div>

          <h4>{{ $t('migration.volumes') }}</h4>
          <p v-if="!plan.volumes.length" class="text-muted text-sm">{{ $t('migration.noVolumes') }}</p>
          <table v-else class="table">
            <thead><tr><th>{{ $t('apps.form.name') }}</th><th>{{ $t('migration.action') }}</th><th>{{ $t('migration.size') }}</th><th>{{ $t('migration.storageClass') }}</th></tr></thead>
            <tbody>
              <tr v-for="v in plan.volumes" :key="v.volume_id">
                <td class="mono">{{ v.name }}</td>
                <td>{{ $t(`migration.volumeAction.${v.action}`) }}</td>
                <td class="text-sm">{{ fmtSize(v.used_bytes) }}</td>
                <td class="text-sm">{{ v.storage_class }}</td>
              </tr>
            </tbody>
          </table>

          <h4>{{ $t('migration.databases') }}</h4>
          <p v-if="!plan.databases.length" class="text-muted text-sm">{{ $t('migration.noDatabases') }}</p>
          <div v-for="d in plan.databases" :key="d.instance_id" class="db-row">
            <div>
              <strong class="mono">{{ d.instance_name }}</strong>
              <span class="text-muted text-sm"> · {{ d.engine }} {{ d.version }} · {{ d.exclusive ? $t('migration.exclusive') : $t('migration.shared') }}</span>
              <div v-if="d.databases.length" class="text-muted text-sm">{{ d.databases.map((x) => x.name).join(', ') }}</div>
            </div>
            <div v-if="d.strategies.length && choices[d.instance_id]" class="db-choice">
              <select v-model="choices[d.instance_id].strategy" class="form-select" @change="runPlan">
                <option v-for="s in d.strategies" :key="s" :value="s">{{ $t(`migration.strategy.${s}`) }}</option>
              </select>
              <select
                v-if="choices[d.instance_id].strategy === 'existing_instance'"
                v-model="choices[d.instance_id].target"
                class="form-select"
                @change="runPlan"
              >
                <option :value="undefined" disabled>{{ $t('migration.chooseInstance') }}</option>
                <option v-for="i in candidates(d.engine)" :key="i.id" :value="i.id">{{ i.display_name || i.name }} ({{ i.version }})</option>
              </select>
            </div>
          </div>

          <h4>{{ $t('migration.cutoverHeading') }}</h4>
          <label class="toggle-row">
            <input v-model="cutoverMode" type="radio" value="auto" />
            <span>{{ $t('migration.cutoverAuto') }}</span>
          </label>
          <label class="toggle-row">
            <input v-model="cutoverMode" type="radio" value="manual" />
            <span>{{ $t('migration.cutoverManual') }}</span>
          </label>
          <div class="form-group" style="margin-top: 12px">
            <label class="form-label">{{ $t('migration.bandwidth') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
            <input v-model.number="bandwidthMB" type="number" min="0" step="1" class="form-input" style="max-width: 160px" placeholder="MB/s" />
            <p class="form-hint">{{ $t('migration.bandwidthHint') }}</p>
          </div>

          <div class="note">
            <span class="mdi mdi-information-outline"></span>
            <div>{{ $t('migration.noEncryption') }}</div>
          </div>

          <div class="form-group" style="margin-top: 12px">
            <label class="form-label">{{ $t('migration.typeName', { app: appName }) }}</label>
            <input v-model="confirmName" class="form-input mono" :placeholder="appName" />
          </div>
        </template>
      </div>
      <div class="modal-footer">
        <span v-if="plan && plan.copy_bytes" class="text-muted text-sm footer-note">{{ $t('migration.copySize', { size: fmtSize(plan.copy_bytes) }) }}</span>
        <button class="btn btn-secondary" @click="wizardOpen = false">{{ $t('action.cancel') }}</button>
        <button class="btn btn-primary" :disabled="!canStart || starting" @click="start">
          {{ starting ? $t('migration.starting') : $t('migration.start') }}
        </button>
      </div>
    </AppModal>
  </Teleport>

  <ConfirmDialog
    :open="!!confirming"
    :title="confirmCopy.title"
    :message="confirmCopy.message"
    :variant="confirmCopy.variant"
    :busy="acting"
    @confirm="act"
    @cancel="confirming = null"
  />
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.migration {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.migration-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.phases {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  list-style: none;
  margin: 0;
  padding: 0;
  font-size: 12px;
}
.phases li {
  padding: 3px 8px;
  border-radius: 999px;
  border: 1px solid var(--border);
  color: var(--text-muted);
}
.phases li.done {
  color: var(--text-secondary, var(--text));
  background: var(--surface-2, var(--bg-subtle));
}
.phases li.active {
  color: var(--primary-700, var(--primary));
  border-color: var(--primary-300, var(--primary));
  font-weight: 600;
}
.items {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.item {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.bar {
  flex-basis: 100%;
  height: 4px;
  background: var(--surface-2, var(--bg-subtle));
  border-radius: 2px;
  overflow: hidden;
}
.bar div {
  height: 100%;
  background: var(--primary);
}
.grace,
.note {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  font-size: 0.9em;
}
.grace p {
  margin: 0 0 6px;
}
.actions {
  display: flex;
  gap: 8px;
}
.history {
  margin-top: 16px;
}
.history-row {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  padding: 4px 0;
}
.issues {
  padding: 10px 12px;
  border-radius: 6px;
  margin-bottom: 12px;
  font-size: 0.9em;
}
.issues ul {
  margin: 6px 0 0;
  padding-left: 18px;
}
.blockers {
  background: var(--danger-50);
  color: var(--danger-700);
}
.warnings {
  background: var(--warning-50);
  color: var(--warning-700);
}
.db-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
}
.db-choice {
  display: flex;
  gap: 8px;
}
.toggle-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.notes {
  margin: 0;
  padding-left: 18px;
}
.danger-text {
  color: var(--danger-700);
}
.footer-note {
  margin-right: auto;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
h4 {
  margin: 16px 0 8px;
}
</style>
