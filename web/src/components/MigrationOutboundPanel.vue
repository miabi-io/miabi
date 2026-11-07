<script setup lang="ts">
// Without wsId it is the install's instance migration (platform admin).
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  migrationApi,
  isRunActive,
  MIGRATION_EXPIRY_HOURS,
  type MigrationOverview,
  type MigrationStarted,
} from '@/api/workspaceMigration'
import { useNotificationStore } from '@/stores/notification'
import { useWorkspaceStore } from '@/stores/workspace'
import { bundleStatusClass } from '@/utils/bundleRun'
import { fmtDateTime } from '@/utils/datetime'
import { relativeTime } from '@/utils/time'
import { copyText } from '@/utils/clipboard'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const props = defineProps<{ wsId?: number }>()

const { t } = useI18n()
const notify = useNotificationStore()
const ws = useWorkspaceStore()

const instance = computed(() => props.wsId === undefined)
const overview = ref<MigrationOverview | null>(null)
const hours = ref(24)
const busy = ref(false)
const started = ref<MigrationStarted | null>(null)
const confirm = ref<'restart' | 'cancel' | 'unfreeze' | null>(null)

async function load() {
  try {
    const res = instance.value ? await migrationApi.instance() : await migrationApi.workspace(props.wsId!)
    overview.value = res.data.data
  } catch (e) {
    notify.apiError(e)
  }
}

// While a code is out, the other Miabi may start pulling at any moment.
const live = computed(() => !!overview.value?.window || !!overview.value?.exports.some(isRunActive))
let poll: ReturnType<typeof setInterval> | null = null
onMounted(async () => {
  await load()
  poll = setInterval(() => {
    if (live.value) load()
  }, 5000)
})
onUnmounted(() => {
  if (poll) clearInterval(poll)
})

async function start() {
  busy.value = true
  try {
    const res = instance.value
      ? await migrationApi.startInstance(hours.value)
      : await migrationApi.startWorkspace(props.wsId!, hours.value)
    started.value = res.data.data
    notify.success(t('wsMigration.startedOk'))
    confirm.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

async function cancel() {
  busy.value = true
  try {
    if (instance.value) await migrationApi.cancelInstance()
    else await migrationApi.cancelWorkspace(props.wsId!)
    started.value = null
    notify.success(t('wsMigration.cancelled'))
    confirm.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

async function unfreeze() {
  busy.value = true
  try {
    await migrationApi.unfreeze(props.wsId!)
    notify.success(t('wsMigration.unfrozen'))
    confirm.value = null
    await Promise.all([load(), ws.fetchWorkspaces().catch(() => {})])
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

async function copyCode() {
  if (!started.value) return
  if (await copyText(started.value.code)) notify.success(t('wsMigration.copied'))
  else notify.error(t('notify.apiKeys.copyFailedSelectAndCopy'))
}

const confirmCopy = computed(() => {
  switch (confirm.value) {
    case 'restart':
      return { title: t('wsMigration.restartTitle'), message: t('wsMigration.restartMessage'), label: t('wsMigration.restart'), run: start }
    case 'cancel':
      return { title: t('wsMigration.cancelTitle'), message: t('wsMigration.cancelMessage'), label: t('wsMigration.cancel'), run: cancel }
    default:
      return { title: t('wsMigration.unfreezeTitle'), message: t('wsMigration.unfreezeMessage'), label: t('wsMigration.unfreeze'), run: unfreeze }
  }
})
</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>{{ instance ? $t('wsMigration.moveInstanceTitle') : $t('wsMigration.moveTitle') }}</h2>
    </div>
    <div v-if="overview" class="card-body stack">
      <div v-if="overview.frozen" class="app-banner app-banner--warning">
        <span class="mdi mdi-snowflake app-banner-icon"></span>
        <div class="app-banner-content">
          <p class="app-banner-title">{{ $t('operator.restriction.frozenTitle') }}</p>
          <p class="app-banner-text">
            {{ $t('operator.restriction.frozenBody') }}
            <template v-if="overview.frozen_reason"> {{ $t('operator.restriction.reason', { reason: overview.frozen_reason }) }}</template>
          </p>
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="confirm = 'unfreeze'">
            <span class="mdi mdi-backup-restore"></span>{{ $t('wsMigration.unfreezeRollback') }}</button>
        </div>
      </div>

      <div v-if="started" class="code-card">
        <div class="app-banner app-banner--warning">
          <span class="mdi mdi-alert-outline app-banner-icon"></span>
          <div class="app-banner-content">
            <p class="app-banner-title">{{ $t('wsMigration.codeShownOnceTitle') }}</p>
            <p class="app-banner-text">{{ instance ? $t('wsMigration.instanceCodeShownOnce') : $t('wsMigration.codeShownOnce') }}</p>
          </div>
        </div>
        <span class="form-label">{{ $t('wsMigration.code') }}</span>
        <div class="code-row">
          <code class="code-block">{{ started.code }}</code>
          <button type="button" class="btn btn-primary btn-sm" @click="copyCode">
            <span class="mdi mdi-content-copy"></span>{{ $t('action.copy') }}</button>
        </div>
        <p class="text-sm">
          <strong>{{ instance ? $t('wsMigration.instanceCodeNext') : $t('wsMigration.codeNext') }}</strong>
        </p>
        <p class="text-muted text-sm">{{ $t('wsMigration.expiresAt', { date: fmtDateTime(started.expires_at) }) }}</p>
        <button type="button" class="btn btn-secondary btn-sm" @click="started = null">
          <span class="mdi mdi-check"></span>{{ $t('wsMigration.copiedDone') }}</button>
      </div>

      <div v-else-if="overview.window" class="window-card">
        <div class="window-head">
          <span class="pulse"></span>
          <strong>{{ $t('wsMigration.windowOpen') }}</strong>
        </div>
        <p class="text-muted text-sm">{{ instance ? $t('wsMigration.windowOpenInstanceHint') : $t('wsMigration.windowOpenHint') }}</p>
        <dl class="window-facts text-sm">
          <dt>{{ $t('wsMigration.col.started') }}</dt>
          <dd>{{ fmtDateTime(overview.window.started_at) }}</dd>
          <dt>{{ $t('wsMigration.col.expires') }}</dt>
          <dd>{{ fmtDateTime(overview.window.expires_at) }}</dd>
          <dt>{{ $t('wsMigration.col.lastUsed') }}</dt>
          <dd>{{ overview.window.last_used_at ? relativeTime(overview.window.last_used_at) : $t('wsMigration.notUsedYet') }}</dd>
        </dl>
        <div class="actions">
          <button class="btn btn-danger btn-sm" :disabled="busy" @click="confirm = 'cancel'">
            <span class="mdi mdi-close-circle-outline"></span>{{ $t('wsMigration.cancel') }}</button>
          <select v-model.number="hours" class="form-select" :aria-label="$t('wsMigration.expiresIn')">
            <option v-for="h in MIGRATION_EXPIRY_HOURS" :key="h" :value="h">{{ $t(`wsMigration.expiryOption.h${h}`) }}</option>
          </select>
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="confirm = 'restart'">
            <span class="mdi mdi-refresh"></span>{{ $t('wsMigration.restart') }}</button>
        </div>
      </div>

      <div v-else-if="!overview.frozen" class="start">
        <p class="text-muted text-sm intro">{{ instance ? $t('wsMigration.moveInstanceIntro') : $t('wsMigration.moveIntro') }}</p>
        <ol v-if="!instance" class="text-sm steps">
          <li>{{ $t('wsMigration.moveStep1') }}</li>
          <li>{{ $t('wsMigration.moveStep2') }}</li>
          <li>{{ $t('wsMigration.moveStep3') }}</li>
        </ol>
        <form class="start-row" @submit.prevent="start">
          <div class="form-group">
            <label class="form-label" for="mo-exp">{{ $t('wsMigration.expiresIn') }}</label>
            <select id="mo-exp" v-model.number="hours" class="form-select">
              <option v-for="h in MIGRATION_EXPIRY_HOURS" :key="h" :value="h">{{ $t(`wsMigration.expiryOption.h${h}`) }}</option>
            </select>
          </div>
          <button type="submit" class="btn btn-primary" :disabled="busy">
            <span class="mdi" :class="busy ? 'mdi-loading mdi-spin' : 'mdi-play'"></span>
            {{ instance ? $t('wsMigration.startInstance') : $t('wsMigration.start') }}
          </button>
        </form>
        <small class="form-hint">{{ instance ? $t('wsMigration.instanceExpiresHint') : $t('wsMigration.expiresHint') }}</small>
      </div>

      <div>
        <h3 class="sub-title">{{ $t('wsMigration.exportsTitle') }}</h3>
        <p v-if="!overview.exports.length" class="text-muted text-sm">
          {{ instance ? $t('wsMigration.noExportsInstance') : $t('wsMigration.noExports') }}</p>
        <div v-else class="table-wrapper">
          <table class="table">
            <thead>
              <tr>
                <th v-if="instance">{{ $t('wsMigration.col.workspace') }}</th>
                <th>{{ $t('dashboard.col.status') }}</th>
                <th>{{ $t('wsMigration.col.options') }}</th>
                <th>{{ $t('portable.when') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in overview.exports" :key="e.id">
                <td v-if="instance" class="mono">{{ e.workspace }}</td>
                <td>
                  <span class="badge" :class="bundleStatusClass(e.status)">{{ $t(`wsMigration.status.${e.status}`) }}</span>
                  <span v-if="e.phase && isRunActive(e)" class="text-muted text-sm"> · {{ $t(`wsMigration.phase.${e.phase}`) }}</span>
                  <div v-if="e.completed" class="text-sm imported">
                    <span class="mdi mdi-check-circle"></span>
                    {{ e.imported_by ? $t('wsMigration.importedBy', { url: e.imported_by }) : $t('wsMigration.imported') }}
                  </div>
                  <div v-if="e.error" class="text-sm text-danger">{{ e.error }}</div>
                </td>
                <td>
                  <span v-if="e.freeze" class="badge badge-info">{{ $t('wsMigration.flag.freeze') }}</span>
                  <span class="badge badge-neutral">{{ e.data ? $t('wsMigration.flag.data') : $t('wsMigration.flag.configOnly') }}</span>
                </td>
                <td class="text-sm" :title="fmtDateTime(e.created_at)">{{ relativeTime(e.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :open="!!confirm"
      :title="confirmCopy.title"
      :message="confirmCopy.message"
      :confirm-label="confirmCopy.label"
      :variant="confirm === 'cancel' ? 'danger' : 'primary'"
      :busy="busy"
      @confirm="confirmCopy.run"
      @cancel="confirm = null"
    />
  </div>
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.stack {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.app-banner .btn {
  margin-top: 8px;
}
.intro {
  margin-top: 0;
}
.steps {
  margin: 8px 0 14px;
  padding-left: 20px;
  line-height: 1.7;
}
.start-row {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  flex-wrap: wrap;
}
.start-row .form-group {
  margin-bottom: 0;
  min-width: 160px;
}
.code-card,
.window-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 14px 16px;
  border: 1px solid var(--border-primary);
  border-radius: 10px;
}
.code-card > .btn,
.code-card p {
  margin: 0;
}
.code-card > .btn {
  align-self: flex-start;
}
.code-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.code-row .code-block {
  flex: 1;
  min-width: 0;
  padding: 10px 14px;
  word-break: break-all;
  white-space: pre-wrap;
  user-select: all;
}
.window-head {
  display: flex;
  align-items: center;
  gap: 10px;
}
.window-card p {
  margin: 0;
}
.pulse {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--success-500, #22c55e);
  animation: pulse 1.6s ease-in-out infinite;
}
@keyframes pulse {
  50% {
    opacity: 0.35;
  }
}
@media (prefers-reduced-motion: reduce) {
  .pulse {
    animation: none;
  }
}
.window-facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 16px;
  margin: 4px 0;
}
.window-facts dt {
  color: var(--text-muted);
}
.window-facts dd {
  margin: 0;
}
.actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.actions select {
  width: auto;
  margin-left: auto;
}
.sub-title {
  margin: 0 0 8px;
  font-size: 14px;
  font-weight: 600;
}
.imported {
  margin-top: 4px;
  color: var(--success-600, var(--text-primary));
  word-break: break-all;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
</style>
