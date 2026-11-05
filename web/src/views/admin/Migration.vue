<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
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
import { bundleStatusClass } from '@/utils/bundleRun'
import { relativeTime } from '@/utils/time'
import { fmtSize } from '@/utils/format'
import AppModal from '@/components/AppModal.vue'
import MigrationOutboundPanel from '@/components/MigrationOutboundPanel.vue'
import MigrationCodeFields from '@/components/MigrationCodeFields.vue'
import MigrationRunReport from '@/components/MigrationRunReport.vue'

const { t } = useI18n()
const notify = useNotificationStore()

const status = ref<MigrationStatus | null>(null)
const runs = ref<BundleRun[]>([])
const loadingRuns = ref(false)

async function loadStatus() {
  try {
    status.value = (await migrationApi.status()).data.data
  } catch (e) {
    notify.apiError(e)
  }
}

async function loadRuns() {
  loadingRuns.value = true
  try {
    runs.value = (await migrationApi.listAll()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loadingRuns.value = false
  }
}

const anyActive = computed(() => runs.value.some(isRunActive))
let poll: ReturnType<typeof setInterval> | null = null
onMounted(async () => {
  await Promise.all([loadStatus(), loadRuns()])
  poll = setInterval(() => {
    if (anyActive.value) loadRuns()
  }, 5000)
})
onUnmounted(() => {
  if (poll) clearInterval(poll)
})

const source = ref<MigrationSource>(emptySource())
const probing = ref(false)
const info = ref<MigrationSourceInfo | null>(null)
const selected = ref<string[]>([])
const options = ref({ restoreData: true, deployApps: false, freeze: true, retireSource: false })
const starting = ref(false)
const batchErrors = ref<Record<string, string>>({})

async function probe() {
  probing.value = true
  info.value = null
  batchErrors.value = {}
  try {
    const res = (await migrationApi.probe(trimSource(source.value))).data.data
    info.value = res
    selected.value = res.code_workspace ? [res.code_workspace] : (res.workspaces?.map((w) => w.name) ?? [])
  } catch (e) {
    notify.apiError(e)
  } finally {
    probing.value = false
  }
}

const allSelected = computed(() => !!info.value?.workspaces?.length && selected.value.length === info.value.workspaces.length)
function toggleAll() {
  selected.value = allSelected.value ? [] : (info.value?.workspaces?.map((w) => w.name) ?? [])
}

async function startBatch() {
  starting.value = true
  batchErrors.value = {}
  try {
    // An empty list means "every workspace the code opens", so a full selection is sent as one.
    const res = (await migrationApi.startBatch({
      ...trimSource(source.value),
      workspaces: allSelected.value ? [] : selected.value,
      restore_data: options.value.restoreData,
      deploy_apps: options.value.deployApps,
      freeze: options.value.freeze,
      retire_source: options.value.retireSource,
    })).data.data
    batchErrors.value = res.errors ?? {}
    notify.success(t('wsMigration.batchStarted', { count: res.runs?.length ?? 0 }))
    if (!Object.keys(batchErrors.value).length) {
      info.value = null
      source.value = emptySource()
    }
    await loadRuns()
  } catch (e) {
    notify.apiError(e)
  } finally {
    starting.value = false
  }
}

watch(() => source.value.code, () => {
  info.value = null
})

const openRun = ref<BundleRun | null>(null)
async function showRun(r: BundleRun) {
  try {
    openRun.value = (await migrationApi.get(r.id)).data.data
  } catch (e) {
    notify.apiError(e)
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('wsMigration.adminTitle') }}</h1>
        <p class="text-muted">{{ $t('wsMigration.adminSubtitle') }}</p>
      </div>
      <span v-if="status" class="badge" :class="status.enabled ? 'badge-success' : 'badge-neutral'" :title="$t('wsMigration.switchHint')">
        {{ status.enabled ? $t('wsMigration.enabledBadge') : $t('wsMigration.disabledBadge') }}</span>
    </div>

    <div v-if="status" class="stack">
      <div v-if="!status.enabled" class="app-banner app-banner--info">
        <span class="mdi mdi-information-outline app-banner-icon"></span>
        <div class="app-banner-content">
          <p class="app-banner-title">{{ $t('wsMigration.disabledTitle') }}</p>
          <p class="app-banner-text">{{ $t('wsMigration.disabledHint') }}</p>
        </div>
      </div>
      <template v-else>
        <MigrationOutboundPanel />

        <div class="card">
          <div class="card-header">
            <div>
              <h2>{{ $t('wsMigration.importWorkspaces') }}</h2>
              <p class="text-muted text-sm" style="margin: 4px 0 0">{{ $t('wsMigration.importWorkspacesHint') }}</p>
            </div>
          </div>
          <div class="card-body">
            <form @submit.prevent="probe">
              <MigrationCodeFields v-model="source" :disabled="probing || starting" />
              <button type="submit" class="btn btn-secondary" :disabled="probing || !source.code.trim()">
                <span class="mdi" :class="probing ? 'mdi-loading mdi-spin' : 'mdi-connection'"></span>
                {{ probing ? $t('wsMigration.probing') : $t('wsMigration.probe') }}
              </button>
            </form>

            <form v-if="info" class="batch" @submit.prevent="startBatch">
              <p class="text-muted text-sm">{{ $t('wsMigration.sourceVersion', { version: info.version }) }}</p>
              <p v-if="!info.workspaces?.length" class="text-muted">{{ $t('wsMigration.noWorkspaces') }}</p>
              <template v-else>
                <div class="table-wrapper">
                  <table class="table">
                    <thead>
                      <tr>
                        <th style="width: 32px">
                          <input type="checkbox" :checked="allSelected" :aria-label="$t('wsMigration.selectAll')" @change="toggleAll" />
                        </th>
                        <th>{{ $t('wsMigration.col.workspace') }}</th>
                        <th class="num">{{ $t('wsMigration.col.apps') }}</th>
                        <th class="num">{{ $t('wsMigration.col.databases') }}</th>
                        <th class="num">{{ $t('wsMigration.col.volumes') }}</th>
                        <th class="num">{{ $t('wsMigration.col.size') }}</th>
                        <th></th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="w in info.workspaces" :key="w.name">
                        <td><input v-model="selected" type="checkbox" :value="w.name" :aria-label="w.name" /></td>
                        <td>
                          <div>{{ w.display_name || w.name }}</div>
                          <div class="mono text-muted text-sm">{{ w.name }}</div>
                        </td>
                        <td class="num">{{ w.apps }}</td>
                        <td class="num">{{ w.databases }}</td>
                        <td class="num">{{ w.volumes }}</td>
                        <td class="num">{{ w.size_bytes ? `~${fmtSize(w.size_bytes)}` : '—' }}</td>
                        <td>
                          <span v-if="w.frozen" class="badge badge-info">{{ $t('operator.restriction.badgeFrozen') }}</span>
                          <span v-if="batchErrors[w.name]" class="badge badge-danger" :title="batchErrors[w.name]">
                            {{ $t('wsMigration.status.failed') }}</span>
                          <div v-if="batchErrors[w.name]" class="text-sm text-muted">{{ batchErrors[w.name] }}</div>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>

                <div class="opts">
                  <label class="opt">
                    <input v-model="options.restoreData" type="checkbox" />
                    <span><strong>{{ $t('wsMigration.opt.copyData') }}</strong>
                      <small>{{ $t('wsMigration.opt.copyDataHint') }}</small></span>
                  </label>
                  <label class="opt">
                    <input v-model="options.freeze" type="checkbox" />
                    <span><strong>{{ $t('wsMigration.opt.freeze') }}</strong>
                      <span class="badge badge-success">{{ $t('wsMigration.recommended') }}</span>
                      <small>{{ $t('wsMigration.opt.freezeHint') }}</small></span>
                  </label>
                  <label class="opt">
                    <input v-model="options.deployApps" type="checkbox" />
                    <span><strong>{{ $t('wsMigration.opt.deploy') }}</strong>
                      <small>{{ $t('wsMigration.opt.deployHint') }}</small></span>
                  </label>
                  <label class="opt">
                    <input v-model="options.retireSource" type="checkbox" />
                    <span><strong>{{ $t('wsMigration.opt.retire') }}</strong>
                      <small>{{ $t('wsMigration.opt.retireHint') }}</small></span>
                  </label>
                </div>
                <p class="text-muted text-sm">{{ $t('wsMigration.batchNotMigrated') }}</p>

                <button type="submit" class="btn btn-primary" :disabled="starting || !selected.length">
                  <span class="mdi" :class="starting ? 'mdi-loading mdi-spin' : 'mdi-import'"></span>
                  {{ starting ? $t('action.working') : $t('wsMigration.startBatch', { count: selected.length }) }}
                </button>
              </template>
            </form>
          </div>
        </div>
      </template>

      <div class="card">
        <div class="card-header">
          <h2>{{ $t('wsMigration.allMigrations') }}</h2>
          <button class="btn btn-secondary btn-sm" :disabled="loadingRuns" @click="loadRuns">
            <span class="mdi" :class="loadingRuns ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>{{ $t('volumes.refresh') }}</button>
        </div>
        <div class="card-body">
          <p v-if="!runs.length" class="text-muted text-sm">{{ $t('wsMigration.noMigrations') }}</p>
          <table v-else class="table">
            <thead>
              <tr>
                <th>{{ $t('wsMigration.col.source') }}</th>
                <th>{{ $t('wsMigration.col.target') }}</th>
                <th>{{ $t('dashboard.col.status') }}</th>
                <th>{{ $t('wsMigration.col.batch') }}</th>
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
                  <RouterLink v-if="r.target_workspace_id" :to="`/admin/workspaces/${r.target_workspace_id}`">
                    #{{ r.target_workspace_id }}</RouterLink>
                </td>
                <td>
                  <span class="badge" :class="bundleStatusClass(r.status)">{{ $t(`wsMigration.status.${r.status}`) }}</span>
                  <span v-if="r.migration?.rehearse" class="badge badge-info">{{ $t('wsMigration.rehearsal') }}</span>
                  <span v-if="r.phase && isRunActive(r)" class="text-muted text-sm"> · {{ $t(`wsMigration.phase.${r.phase}`) }}</span>
                </td>
                <td class="mono text-sm text-muted">{{ r.migration?.batch_id ? r.migration.batch_id.slice(0, 8) : '—' }}</td>
                <td class="text-sm">{{ relativeTime(r.created_at) }}</td>
                <td class="row-actions">
                  <button class="btn btn-secondary btn-sm" @click="showRun(r)">{{ $t('portable.report') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="openRun" max-width="860px" @close="openRun = null">
        <div class="modal-header">
          <h3>{{ $t('wsMigration.reportTitle', { kind: $t('wsMigration.kind.migration_import') }) }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="openRun = null">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <div class="modal-body">
          <MigrationRunReport :run="openRun" />
        </div>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.batch {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--border-secondary);
}
.opts {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin: 16px 0 12px;
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
.num {
  text-align: right;
  white-space: nowrap;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.row-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
