<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { workspaceKeysApi, type WorkspaceKeysStatus } from '@/api/workspaceKeys'
import { useNotificationStore } from '@/stores/notification'
import { fmtDate } from '@/utils/datetime'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import BetaBadge from '@/components/BetaBadge.vue'

const props = defineProps<{ wsId: number; wsName: string }>()

const { t } = useI18n()
const notify = useNotificationStore()

const status = ref<WorkspaceKeysStatus | null>(null)
const loading = ref(false)
const rotating = ref(false)
const showConfirm = ref(false)
const copied = ref(false)

const active = computed(() => status.value?.sealing_keys.find((k) => k.active) ?? null)
const retired = computed(() => status.value?.sealing_keys.filter((k) => !k.active) ?? [])
const sealCommand = 'echo -n "$VALUE" | miabi secrets seal NAME >> secrets.yaml'

async function load() {
  loading.value = true
  try {
    status.value = (await workspaceKeysApi.status(props.wsId)).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

async function rotate() {
  showConfirm.value = false
  rotating.value = true
  try {
    const res = (await workspaceKeysApi.rotate(props.wsId)).data.data
    if (res.stale_columns?.length) {
      notify.info(t('notify.wsKeys.rotatedKeptOld', { version: res.data_key_version, columns: res.stale_columns.join(', ') }))
    } else {
      notify.success(t('notify.wsKeys.rotated', { version: res.data_key_version, count: res.reencrypted }))
    }
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    rotating.value = false
  }
}

async function copyKey() {
  if (!active.value) return
  try {
    await navigator.clipboard.writeText(active.value.public_key)
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  } catch {
    notify.apiError(new Error(t('wsKeys.copyFailed')))
  }
}

onMounted(load)
</script>

<template>
  <div class="stack">
    <div v-if="loading && !status" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else-if="status">
      <div class="card">
        <div class="card-header">
          <div>
            <h2>{{ $t('wsKeys.encryptionKeys') }}</h2>
            <p class="text-muted text-sm" style="margin: 4px 0 0">{{ $t('wsKeys.subtitle', { months: status.rotation_months }) }}</p>
          </div>
          <button
            class="btn btn-secondary"
            :disabled="!status.can_rotate || rotating"
            :title="status.next_rotation_at ? $t('wsKeys.nextRotationOn', { date: fmtDate(status.next_rotation_at) }) : ''"
            @click="showConfirm = true"
          >
            <span class="mdi" :class="rotating ? 'mdi-loading mdi-spin' : 'mdi-key-change'"></span>
            {{ rotating ? $t('wsKeys.rotating') : $t('action.rotateKeys') }}
          </button>
        </div>
        <div class="card-body">
          <div v-if="!status.encryption_enabled" class="key-warning" role="status">
            <span class="mdi mdi-lock-open-alert-outline"></span>
            <span>{{ $t('wsKeys.encryptionDisabled') }}</span>
          </div>
          <dl class="key-grid">
            <dt>{{ $t('wsKeys.dataKey') }}</dt>
            <dd>
              <span v-if="status.data_key_version" class="badge badge-neutral mono">v{{ status.data_key_version }}</span>
              <span v-else class="text-muted">{{ $t('wsKeys.notCreatedYet') }}</span>
            </dd>
            <dt>{{ $t('wsKeys.sealingKey') }}</dt>
            <dd><span v-if="active" class="badge badge-neutral mono">v{{ active.version }}</span></dd>
            <dt>{{ $t('wsKeys.lastRotation') }}</dt>
            <dd>{{ status.last_rotated_at ? fmtDate(status.last_rotated_at) : $t('wsKeys.never') }}</dd>
            <dt>{{ $t('wsKeys.nextRotation') }}</dt>
            <dd>
              <span v-if="status.next_rotation_at">{{ fmtDate(status.next_rotation_at) }}</span>
              <span v-else-if="status.can_rotate" class="badge badge-success">{{ $t('wsKeys.availableNow') }}</span>
              <span v-else class="text-muted">{{ $t('wsKeys.unavailable') }}</span>
            </dd>
          </dl>
          <p class="text-muted text-sm" style="margin: 12px 0 0">{{ $t('wsKeys.rotationExplained') }}</p>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div>
            <h2>{{ $t('wsKeys.sealedSecrets') }}<BetaBadge component="sealed-secrets" /></h2>
            <p class="text-muted text-sm" style="margin: 4px 0 0">{{ $t('wsKeys.sealedSubtitle') }}</p>
          </div>
        </div>
        <div class="card-body">
          <template v-if="active">
            <label class="text-sm text-muted">{{ $t('wsKeys.publicKey', { version: active.version }) }}</label>
            <div class="key-row">
              <code class="mono key-value">{{ active.public_key }}</code>
              <button class="btn btn-secondary btn-sm" @click="copyKey">
                <span class="mdi" :class="copied ? 'mdi-check' : 'mdi-content-copy'"></span>{{ copied ? $t('wsKeys.copied') : $t('wsKeys.copy') }}
              </button>
            </div>
          </template>

          <label class="text-sm text-muted" style="display: block; margin-top: 16px">{{ $t('wsKeys.sealFromCli') }}</label>
          <pre class="mono key-cli">{{ sealCommand }}</pre>

          <p class="text-sm" style="margin: 16px 0 0">
            {{ $t('wsKeys.sealedCount', status.sealed_secrets) }}
          </p>
          <div v-if="status.outdated_sealed_secrets" class="key-warning" role="status" style="margin-top: 10px">
            <span class="mdi mdi-information-outline"></span>
            <span>{{ $t('wsKeys.outdatedSealed', status.outdated_sealed_secrets) }}</span>
          </div>

          <table v-if="retired.length" class="table" style="margin-top: 16px">
            <thead>
              <tr>
                <th>{{ $t('wsKeys.olderKeys') }}</th>
                <th>{{ $t('wsKeys.created') }}</th>
                <th>{{ $t('wsKeys.stillInUse') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="k in retired" :key="k.version">
                <td class="mono">v{{ k.version }}</td>
                <td>{{ fmtDate(k.created_at) }}</td>
                <td>{{ k.sealed_secrets }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="showConfirm"
      :title="$t('confirm.title.rotateWorkspaceKeys')"
      :message="$t('confirm.message.wsKeys.rotate', { name: wsName, months: status?.rotation_months ?? 6 })"
      :confirm-label="$t('action.rotateKeys')"
      variant="danger"
      :busy="rotating"
      @confirm="rotate"
      @cancel="showConfirm = false"
    />
  </div>
</template>

<style scoped>
.stack { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.key-grid { display: grid; grid-template-columns: max-content 1fr; gap: 10px 24px; margin: 0; font-size: 14px; }
.key-grid dt { color: var(--text-muted); }
.key-grid dd { margin: 0; }
.key-row { display: flex; align-items: center; gap: 8px; margin-top: 6px; }
.key-value {
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
  color: var(--text-primary);
  font-size: 12.5px;
  overflow-wrap: anywhere;
}
.key-cli {
  margin: 6px 0 0;
  padding: 10px 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
  color: var(--text-primary);
  font-size: 12.5px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.key-warning {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-bottom: 14px;
  padding: 10px 12px;
  border: 1px solid var(--warning-600);
  border-radius: var(--radius);
  background: var(--warning-50);
  font-size: 13px;
}
.key-warning .mdi { color: var(--warning-600); font-size: 18px; line-height: 1.2; }
</style>
