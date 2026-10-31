<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { pipelineApi } from '@/api/pipelines'
import AppModal from '@/components/AppModal.vue'
import type { PipelineDefinition, PipelineRun, VersionBump, VersionPolicy } from '@/api/types'

// Starts a run with a one-off version: a larger bump than the policy's, or an exact version. Pushes
// and schedules never take this path; they follow the policy.
const props = defineProps<{ pipeline: PipelineDefinition | null; policy: VersionPolicy | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'started', run: PipelineRun): void }>()

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

type Mode = 'default' | VersionBump | 'exact'
const mode = ref<Mode>('default')
const exact = ref('')
const busy = ref(false)
watch(() => props.pipeline, () => { mode.value = 'default'; exact.value = '' })

const semver = computed(() => props.policy?.scheme === 'semver')
// Which bumps the series leaves room for: 1.x.x fixes the major, 1.4.x the minor too.
const fixedParts = computed(() => (props.policy?.series ?? 'x.x.x').split('.').filter((p) => p !== 'x').length)
const allowed = (b: VersionBump) => (b === 'major' ? fixedParts.value === 0 : b === 'minor' ? fixedParts.value <= 1 : true)

async function start() {
  const wid = currentWorkspaceId.value
  if (!wid || !props.pipeline) return
  busy.value = true
  try {
    const input = mode.value === 'exact' ? { version: exact.value.trim() }
      : mode.value === 'default' ? {} : { bump: mode.value }
    const run = (await pipelineApi.trigger(wid, props.pipeline.id, input)).data.data
    notify.success(t('notify.pipelines.runNumberQueued', { number: run.number }), {
      detail: run.image_tag ? t('versioning.runBuilds', { tag: run.image_tag }) : undefined,
    })
    emit('started', run)
    emit('close')
  } catch (e) {
    notify.apiError(e, 'Could not trigger run')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="pipeline && policy" @close="emit('close')">
      <div class="modal-header">
        <h3>{{ $t('versioning.runWithVersion') }}</h3>
        <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="emit('close')"><span class="mdi mdi-close"></span></button>
      </div>
      <form @submit.prevent="start">
        <div class="modal-body">
          <p class="lead">
            <i18n-t keypath="versioning.runLead" tag="span">
              <template #tag><code class="tag">{{ policy.next || 'run-N' }}</code></template>
              <template #series><code>{{ policy.series || policy.calver_format }}</code></template>
            </i18n-t>
          </p>
          <div v-if="semver" class="options" role="radiogroup">
            <label class="option"><input v-model="mode" type="radio" value="default" /> {{ $t('versioning.useDefault', { bump: policy.bump }) }}</label>
            <label v-for="b in (['patch', 'minor', 'major'] as VersionBump[])" :key="b" class="option" :class="{ disabled: !allowed(b) }"
              :title="allowed(b) ? '' : $t('versioning.bumpNotOnSeries', { series: policy.series })">
              <input v-model="mode" type="radio" :value="b" :disabled="!allowed(b)" /> {{ $t(`versioning.bumps.${b}`) }}
            </label>
            <label class="option" :class="{ disabled: !ws.isWorkspaceAdmin }" :title="ws.isWorkspaceAdmin ? '' : $t('versioning.exactAdminOnly')">
              <input v-model="mode" type="radio" value="exact" :disabled="!ws.isWorkspaceAdmin" /> {{ $t('versioning.exact') }}
            </label>
            <input v-if="mode === 'exact'" v-model="exact" class="form-input mono" placeholder="2.0.0" required :aria-label="$t('versioning.exact')" />
          </div>
          <p v-else class="form-hint">{{ $t('versioning.noOverrides') }}</p>
          <p class="form-hint">{{ $t('versioning.runHintTagOnSuccess') }}</p>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="emit('close')">{{ $t('action.cancel') }}</button>
          <button type="submit" class="btn btn-primary" :disabled="busy">
            <span class="mdi" :class="busy ? 'mdi-loading mdi-spin' : 'mdi-play'"></span>{{ $t('jobs.runNow') }}</button>
        </div>
      </form>
    </AppModal>
  </Teleport>
</template>

<style scoped>
.lead { font-size: 13px; margin-bottom: 12px; }
.tag { font-family: 'JetBrains Mono', monospace; padding: 1px 6px; border-radius: 4px; background: var(--primary-50, rgba(99, 102, 241, 0.12)); }
.options { display: flex; flex-direction: column; gap: 8px; margin-bottom: 10px; }
.option { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.option.disabled { opacity: 0.5; cursor: not-allowed; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
