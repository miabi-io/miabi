<script setup lang="ts">
// The run id lives in ?run= so a reload or shared link returns to the progress view.
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  migrationApi,
  isRunActive,
  emptySource,
  trimSource,
  type MigrationSource,
  type MigrationSourceInfo,
  type MigrationStatus,
} from '@/api/workspaceMigration'
import type { BundleRun } from '@/api/portableBackup'
import { useNotificationStore } from '@/stores/notification'
import { useWorkspaceStore } from '@/stores/workspace'
import { useAuthStore } from '@/stores/auth'
import { bundleStatusClass } from '@/utils/bundleRun'
import { relativeTime } from '@/utils/time'
import { fmtSize } from '@/utils/format'
import MigrationCodeFields from '@/components/MigrationCodeFields.vue'
import MigrationRunReport from '@/components/MigrationRunReport.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const ws = useWorkspaceStore()
const auth = useAuthStore()

const status = ref<MigrationStatus | null>(null)
const source = ref<MigrationSource>(emptySource())
const probing = ref(false)
const info = ref<MigrationSourceInfo | null>(null)

const options = ref({
  sourceWorkspace: '',
  newWorkspace: '',
  restoreData: true,
  deployApps: false,
  freeze: true,
  rehearse: false,
  retireSource: false,
})
const starting = ref(false)

const runs = ref<BundleRun[]>([])
const run = ref<BundleRun | null>(null)
const runId = computed(() => (route.query.run ? Number(route.query.run) : null))
let poll: ReturnType<typeof setInterval> | null = null

async function loadStatus() {
  try {
    status.value = (await migrationApi.status()).data.data
  } catch (e) {
    notify.apiError(e)
  }
}

async function loadRuns() {
  try {
    runs.value = (await migrationApi.list()).data.data ?? []
  } catch {
    runs.value = []
  }
}

async function probe() {
  probing.value = true
  info.value = null
  try {
    const res = (await migrationApi.probe(trimSource(source.value))).data.data
    info.value = res
    const first = res.workspaces?.find((w) => w.name === res.code_workspace) ?? res.workspaces?.[0]
    if (first) pick(first.name)
  } catch (e) {
    notify.apiError(e)
  } finally {
    probing.value = false
  }
}

function pick(name: string) {
  options.value.sourceWorkspace = name
  options.value.newWorkspace = name
}

const selectedWs = computed(() => info.value?.workspaces?.find((w) => w.name === options.value.sourceWorkspace) ?? null)
// A workspace code opens only its own workspace; an instance code lets the importer pick.
const pickable = computed(() => !info.value?.code_workspace && (info.value?.workspaces?.length ?? 0) > 1)

// The preview belongs to the code that was checked.
watch(() => source.value.code, () => {
  info.value = null
})

async function start() {
  const o = options.value
  starting.value = true
  try {
    const res = (await migrationApi.start({
      ...trimSource(source.value),
      source_workspace: o.sourceWorkspace,
      new_workspace: o.newWorkspace.trim(),
      restore_data: !o.rehearse && o.restoreData,
      deploy_apps: !o.rehearse && o.deployApps,
      freeze: !o.rehearse && o.freeze,
      rehearse: o.rehearse,
      retire_source: !o.rehearse && o.retireSource,
    })).data.data
    notify.success(t('wsMigration.started'))
    source.value = emptySource()
    info.value = null
    run.value = res
    await router.replace({ query: { run: String(res.id) } })
    await ws.fetchWorkspaces().catch(() => {})
  } catch (e) {
    notify.apiError(e)
  } finally {
    starting.value = false
  }
}

async function loadRun(id: number) {
  try {
    run.value = (await migrationApi.get(id)).data.data
  } catch (e) {
    notify.apiError(e)
    run.value = null
  }
}

function stopPoll() {
  if (poll) clearInterval(poll)
  poll = null
}

function startPoll() {
  stopPoll()
  poll = setInterval(async () => {
    if (!runId.value) return stopPoll()
    await loadRun(runId.value)
    if (run.value && !isRunActive(run.value)) {
      stopPoll()
      await Promise.all([loadRuns(), ws.fetchWorkspaces().catch(() => {})])
    }
  }, 3000)
}

watch(runId, async (id) => {
  stopPoll()
  if (!id) {
    run.value = null
    await loadRuns()
    return
  }
  await loadRun(id)
  if (run.value && isRunActive(run.value)) startPoll()
}, { immediate: true })

onMounted(async () => {
  await Promise.all([loadStatus(), loadRuns()])
})
onUnmounted(stopPoll)

function openWorkspace(id: number) {
  ws.setWorkspace(id)
  router.push('/')
}

function newImport() {
  router.replace({ query: {} })
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/workspaces')">
          <span class="mdi mdi-arrow-left"></span>{{ $t('wsSettings.workspaces') }}</button>
        <h1>{{ $t('switcher.import') }}</h1>
        <p class="text-muted">{{ $t('wsMigration.importSubtitle') }}</p>
      </div>
    </div>

    <div v-if="run" class="card">
      <div class="card-header">
        <h2>{{ $t('wsMigration.progress') }}</h2>
        <button class="btn btn-secondary btn-sm" @click="newImport">
          <span class="mdi mdi-plus"></span>{{ $t('wsMigration.newImport') }}</button>
      </div>
      <div class="card-body">
        <MigrationRunReport :run="run" @open-workspace="openWorkspace" />
      </div>
    </div>

    <template v-else-if="status">
      <div v-if="!status.enabled" class="card">
        <div class="card-body">
          <div class="app-banner app-banner--info">
            <span class="mdi mdi-information-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">{{ $t('wsMigration.disabledTitle') }}</p>
              <p class="app-banner-text">{{ $t('wsMigration.disabledHint') }}</p>
            </div>
          </div>
        </div>
      </div>

      <div v-else-if="!auth.canCreateWorkspace" class="card">
        <div class="card-body">
          <p class="text-muted">{{ $t('wsMigration.cannotCreate') }}</p>
        </div>
      </div>

      <template v-else>
        <div class="card">
          <div class="card-header">
            <h2><span class="step">1</span>{{ $t('wsMigration.stepSource') }}</h2>
          </div>
          <form class="card-body" @submit.prevent="probe">
            <p class="text-muted text-sm" style="margin-top: 0">{{ $t('wsMigration.stepSourceHint') }}</p>
            <MigrationCodeFields v-model="source" :disabled="probing" />
            <button type="submit" class="btn btn-secondary" :disabled="probing || !source.code.trim()">
              <span class="mdi" :class="probing ? 'mdi-loading mdi-spin' : 'mdi-connection'"></span>
              {{ probing ? $t('wsMigration.probing') : $t('wsMigration.probe') }}
            </button>
          </form>
        </div>

        <div v-if="info" class="card">
          <div class="card-header">
            <h2><span class="step">2</span>{{ $t('wsMigration.stepWorkspace') }}</h2>
            <span class="text-muted text-sm">{{ $t('wsMigration.sourceVersion', { version: info.version }) }}</span>
          </div>
          <form class="card-body" @submit.prevent="start">
            <p v-if="!info.workspaces?.length" class="text-muted">{{ $t('wsMigration.noWorkspaces') }}</p>
            <template v-else>
              <div v-if="pickable" class="form-group">
                <span class="form-label">{{ $t('wsMigration.pickWorkspace') }}</span>
                <div class="ws-list" role="radiogroup">
                  <label v-for="w in info.workspaces" :key="w.name" class="ws-option"
                    :class="{ selected: options.sourceWorkspace === w.name }">
                    <input type="radio" name="source-ws" :value="w.name" :checked="options.sourceWorkspace === w.name"
                      @change="pick(w.name)" />
                    <span class="ws-name">{{ w.display_name || w.name }}</span>
                    <span class="mono text-muted text-sm">{{ w.name }}</span>
                    <span v-if="w.frozen" class="badge badge-info">{{ $t('operator.restriction.badgeFrozen') }}</span>
                  </label>
                </div>
              </div>

              <div v-if="selectedWs" class="preview">
                <div class="preview-head">
                  <span class="mdi mdi-briefcase-outline"></span>
                  <span class="ws-name">{{ selectedWs.display_name || selectedWs.name }}</span>
                  <span class="mono text-muted text-sm">{{ selectedWs.name }}</span>
                  <span v-if="selectedWs.frozen" class="badge badge-info">{{ $t('operator.restriction.badgeFrozen') }}</span>
                </div>
                <dl class="preview-stats">
                  <div><dt>{{ $t('wsMigration.col.apps') }}</dt><dd>{{ selectedWs.apps }}</dd></div>
                  <div><dt>{{ $t('wsMigration.col.databases') }}</dt><dd>{{ selectedWs.databases }}</dd></div>
                  <div><dt>{{ $t('wsMigration.col.volumes') }}</dt><dd>{{ selectedWs.volumes }}</dd></div>
                  <div><dt>{{ $t('wsMigration.col.size') }}</dt><dd>{{ selectedWs.size_bytes ? `~${fmtSize(selectedWs.size_bytes)}` : '—' }}</dd></div>
                </dl>
              </div>

              <div class="form-group" style="margin-top: 16px">
                <label class="form-label" for="mi-new">{{ $t('portable.newWorkspaceName') }}</label>
                <input id="mi-new" v-model="options.newWorkspace" class="form-input" required />
                <small class="form-hint">{{ $t('wsMigration.newWorkspaceHint') }}</small>
              </div>

              <div class="opts">
                <label class="opt" :class="{ disabled: options.rehearse }">
                  <input v-model="options.restoreData" type="checkbox" :disabled="options.rehearse" />
                  <span><strong>{{ $t('wsMigration.opt.copyData') }}</strong>
                    <small>{{ $t('wsMigration.opt.copyDataHint') }}</small></span>
                </label>
                <label class="opt" :class="{ disabled: options.rehearse }">
                  <input v-model="options.freeze" type="checkbox" :disabled="options.rehearse" />
                  <span><strong>{{ $t('wsMigration.opt.freeze') }}</strong>
                    <span class="badge badge-success">{{ $t('wsMigration.recommended') }}</span>
                    <small>{{ $t('wsMigration.opt.freezeHint') }}</small></span>
                </label>
                <label class="opt" :class="{ disabled: options.rehearse }">
                  <input v-model="options.deployApps" type="checkbox" :disabled="options.rehearse" />
                  <span><strong>{{ $t('wsMigration.opt.deploy') }}</strong>
                    <small>{{ $t('wsMigration.opt.deployHint') }}</small></span>
                </label>
                <label class="opt" :class="{ disabled: options.rehearse }">
                  <input v-model="options.retireSource" type="checkbox" :disabled="options.rehearse" />
                  <span><strong>{{ $t('wsMigration.opt.retire') }}</strong>
                    <small>{{ $t('wsMigration.opt.retireHint') }}</small></span>
                </label>
                <label class="opt opt-rehearse">
                  <input v-model="options.rehearse" type="checkbox" />
                  <span><strong>{{ $t('wsMigration.opt.rehearse') }}</strong>
                    <small>{{ $t('wsMigration.opt.rehearseHint') }}</small></span>
                </label>
              </div>

              <button type="submit" class="btn btn-primary" :disabled="starting || !options.sourceWorkspace || !options.newWorkspace.trim()">
                <span class="mdi" :class="starting ? 'mdi-loading mdi-spin' : 'mdi-import'"></span>
                {{ starting ? $t('action.working') : options.rehearse ? $t('wsMigration.startRehearsal') : $t('wsMigration.start') }}
              </button>
            </template>
          </form>
        </div>
      </template>
    </template>

    <div v-if="runs.length" class="card">
      <div class="card-header"><h2>{{ $t('wsMigration.yourMigrations') }}</h2></div>
      <div class="card-body">
        <table class="table">
          <thead>
            <tr>
              <th>{{ $t('wsMigration.col.source') }}</th>
              <th>{{ $t('dashboard.col.status') }}</th>
              <th>{{ $t('portable.when') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in runs" :key="r.id">
              <td>
                <div class="mono">{{ r.source_workspace }}</div>
                <div class="text-muted text-sm">{{ r.migration?.source_url }}</div>
              </td>
              <td>
                <span class="badge" :class="bundleStatusClass(r.status)">{{ $t(`wsMigration.status.${r.status}`) }}</span>
                <span v-if="r.migration?.rehearse" class="badge badge-info">{{ $t('wsMigration.rehearsal') }}</span>
                <span v-if="r.phase && isRunActive(r)" class="text-muted text-sm"> · {{ $t(`wsMigration.phase.${r.phase}`) }}</span>
              </td>
              <td class="text-sm">{{ relativeTime(r.created_at) }}</td>
              <td class="row-actions">
                <button class="btn btn-secondary btn-sm" @click="router.replace({ query: { run: String(r.id) } })">
                  {{ $t('portable.report') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.card + .card {
  margin-top: 16px;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.step {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  margin-right: 8px;
  border-radius: 50%;
  background: var(--primary-500);
  color: #fff;
  font-size: 12px;
  font-weight: 700;
}
.ws-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ws-option {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
  cursor: pointer;
}
.ws-option.selected {
  border-color: var(--primary-500);
}
.ws-name {
  font-weight: 600;
}
.preview {
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
}
.preview-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.preview-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(110px, 1fr));
  gap: 12px;
  margin: 12px 0 0;
}
.preview-stats dt {
  color: var(--text-muted);
  font-size: 12px;
}
.preview-stats dd {
  margin: 2px 0 0;
  font-size: 18px;
  font-weight: 600;
}
.opt-rehearse {
  padding-top: 10px;
  border-top: 1px dashed var(--border-primary);
}
.opts {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin: 8px 0 18px;
}
.opt {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  cursor: pointer;
}
.opt input {
  margin-top: 3px;
}
.opt small {
  display: block;
  color: var(--text-muted);
  font-size: 12px;
  margin-top: 2px;
}
.opt .badge {
  margin-left: 6px;
}
.opt.disabled {
  opacity: 0.55;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.row-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
