<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { jobApi } from '@/api/resources'
import type { JobDefinition } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'

const props = defineProps<{ wsId: number; appId: number; canEdit: boolean; managedBy: string }>()

const { t } = useI18n()
const notify = useNotificationStore()

const BUDGET_SECS = 30 * 60

const current = ref<JobDefinition | null>(null)
const form = ref({ command: '', timeout: 300, backoff: 0 })
const saving = ref(false)

// A GitOps or marketplace app declares its Jobs in its source; the API refuses the edit.
const locked = computed(() => props.managedBy === 'gitops' || props.managedBy === 'marketplace')
const editable = computed(() => props.canEdit && !locked.value)
const overBudget = computed(() => form.value.timeout * (form.value.backoff + 1) > BUDGET_SECS)

function fill(def: JobDefinition | null) {
  current.value = def
  form.value = def
    ? { command: def.command.join(' '), timeout: def.timeout_secs, backoff: def.backoff_limit }
    : { command: '', timeout: 300, backoff: 0 }
}

async function load() {
  if (!props.wsId || !props.appId) return
  try {
    fill((await jobApi.preRelease(props.wsId, props.appId)).data.data ?? null)
  } catch (e) {
    notify.apiError(e)
  }
}
watch(() => [props.wsId, props.appId], load, { immediate: true })

async function save() {
  const command = form.value.command.trim().split(/\s+/).filter(Boolean)
  if (command.length === 0) {
    notify.error(t('notify.jobs.aCommandIsRequired'))
    return
  }
  saving.value = true
  try {
    const res = await jobApi.setPreRelease(props.wsId, props.appId, {
      command, timeout_secs: form.value.timeout, backoff_limit: form.value.backoff,
    })
    fill(res.data.data)
    notify.success(t('preRelease.saved'))
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  saving.value = true
  try {
    await jobApi.removePreRelease(props.wsId, props.appId)
    fill(null)
    notify.success(t('preRelease.removed'))
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="card mb-4">
    <div class="card-header">
      <div>
        <h2>{{ $t('preRelease.title') }}</h2>
        <p class="card-subtitle">{{ $t('preRelease.subtitle') }}</p>
      </div>
      <span v-if="current?.last_status" class="badge badge-dot" :class="current.last_status === 'succeeded' ? 'badge-success' : current.last_status === 'failed' ? 'badge-danger' : 'badge-info'">
        {{ $t('preRelease.lastRun', { status: current.last_status }) }}
      </span>
    </div>
    <div class="card-body" style="max-width: 560px">
      <p v-if="locked" class="form-hint"><span class="mdi mdi-lock-outline"></span> {{ $t('preRelease.locked') }}</p>
      <div class="form-group">
        <label class="form-label">{{ $t('preRelease.command') }}</label>
        <input v-model="form.command" class="form-input mono" :disabled="!editable" placeholder="./web migrate up" />
        <p class="form-hint">{{ $t('preRelease.commandHint') }}</p>
      </div>
      <div class="form-row">
        <div class="form-group" style="flex: 1">
          <label class="form-label">{{ $t('preRelease.timeout') }}</label>
          <input v-model.number="form.timeout" type="number" min="1" :max="BUDGET_SECS" class="form-input" :class="{ 'input-error': overBudget }" :disabled="!editable" />
        </div>
        <div class="form-group" style="flex: 1">
          <label class="form-label">{{ $t('preRelease.retries') }}</label>
          <input v-model.number="form.backoff" type="number" min="0" max="10" class="form-input" :class="{ 'input-error': overBudget }" :disabled="!editable" />
        </div>
      </div>
      <p class="form-hint" :class="{ 'hint-error': overBudget }" style="margin-top: -8px">
        {{ overBudget ? $t('preRelease.overBudget', { max: BUDGET_SECS }) : $t('preRelease.failHint') }}
      </p>
      <div v-if="editable" class="btn-row">
        <button class="btn btn-primary" :disabled="saving || overBudget" @click="save">
          {{ saving ? $t('action.saving') : $t('preRelease.save') }}
        </button>
        <button v-if="current" class="btn btn-ghost" :disabled="saving" @click="remove">{{ $t('preRelease.remove') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.btn-row { display: flex; gap: 8px; }
</style>
