<script setup lang="ts">
import { computed } from 'vue'
import type { BundleRun } from '@/api/portableBackup'
import { isRunActive } from '@/api/workspaceMigration'
import { bundleStatusClass, reportActionClass } from '@/utils/bundleRun'
import { fmtSize } from '@/utils/format'
import { fmtDateTime } from '@/utils/datetime'

const props = defineProps<{ run: BundleRun }>()
defineEmits<{ (e: 'open-workspace', id: number): void }>()

// The source freezes inside "transfer"; the target never reports a freeze phase of its own.
const phases = ['transfer', 'state', 'databases', 'volumes', 'done'] as const

const active = computed(() => isRunActive(props.run))
const finished = computed(() => !active.value)
// Without data the restore never enters these phases, so they would sit unticked forever.
const shownPhases = computed(() => phases.filter((p) => props.run.restore_data || (p !== 'databases' && p !== 'volumes')))
const phaseIndex = computed(() => shownPhases.value.indexOf(props.run.phase as (typeof phases)[number]))

function phaseState(i: number): 'done' | 'current' | 'todo' {
  if (props.run.status === 'completed' || props.run.status === 'partial') return 'done'
  if (i < phaseIndex.value) return 'done'
  if (i === phaseIndex.value) return 'current'
  return 'todo'
}
</script>

<template>
  <div class="run-report">
    <div class="run-head">
      <span class="badge" :class="bundleStatusClass(run.status)">{{ $t(`wsMigration.status.${run.status}`) }}</span>
      <span v-if="run.migration?.rehearse" class="badge badge-info">{{ $t('wsMigration.rehearsal') }}</span>
      <span class="mono text-sm">{{ run.source_workspace }}</span>
      <span v-if="run.migration?.source_url" class="text-muted text-sm">
        {{ $t('wsMigration.fromSource', { url: run.migration.source_url }) }}
        <template v-if="run.migration.source_version"> · v{{ run.migration.source_version }}</template>
      </span>
      <span v-if="run.migration?.insecure_tls" class="badge badge-warning" :title="$t('wsMigration.skipTlsWarning')">
        {{ $t('wsMigration.tlsUnverified') }}</span>
    </div>

    <ol class="phases">
      <li v-for="(p, i) in shownPhases" :key="p" :class="`phase-${phaseState(i)}`">
        <span class="mdi" :class="phaseState(i) === 'done' ? 'mdi-check-circle' : phaseState(i) === 'current'
          ? (active ? 'mdi-loading mdi-spin' : 'mdi-alert-circle') : 'mdi-circle-outline'"></span>
        {{ $t(`wsMigration.phase.${p}`) }}
      </li>
    </ol>

    <p class="text-muted text-sm">
      <template v-if="run.started_at">{{ $t('wsMigration.startedAt', { date: fmtDateTime(run.started_at) }) }}</template>
      <template v-if="run.finished_at"> · {{ $t('wsMigration.finishedAt', { date: fmtDateTime(run.finished_at) }) }}</template>
      <template v-if="run.size_bytes"> · {{ fmtSize(run.size_bytes) }}</template>
    </p>

    <div v-if="run.error" class="app-banner app-banner--danger">
      <span class="mdi mdi-alert-outline app-banner-icon"></span>
      <div class="app-banner-content"><p class="app-banner-text">{{ run.error }}</p></div>
    </div>

    <div v-if="finished && run.migration" class="source-state text-sm">
      <span v-if="run.migration.source_retired" class="badge badge-neutral">{{ $t('wsMigration.sourceRetired') }}</span>
      <span v-else-if="run.migration.source_unfrozen" class="badge badge-neutral">{{ $t('wsMigration.sourceUnfrozen') }}</span>
    </div>

    <template v-if="finished">
      <ul v-if="run.report?.notes?.length" class="notes">
        <li v-for="(n, i) in run.report.notes" :key="i">{{ n }}</li>
      </ul>
      <div v-if="run.report?.items?.length" class="table-wrapper">
        <table class="table">
          <thead>
            <tr>
              <th>{{ $t('portable.resource') }}</th>
              <th>{{ $t('apps.form.name') }}</th>
              <th>{{ $t('portable.result') }}</th>
              <th>{{ $t('portable.detail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(it, i) in run.report.items" :key="i">
              <td class="text-sm">{{ it.kind }}</td>
              <td class="text-sm mono">{{ it.name }}</td>
              <td><span class="badge" :class="reportActionClass(it.action)">{{ it.action }}</span></td>
              <td class="text-sm text-muted">{{ it.detail }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <button v-if="run.status !== 'failed' && run.target_workspace_id" class="btn btn-primary"
        @click="$emit('open-workspace', run.target_workspace_id)">
        <span class="mdi mdi-arrow-right"></span>{{ $t('wsMigration.openWorkspace') }}</button>
    </template>
  </div>
</template>

<style scoped>
.run-report {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.run-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.phases {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 18px;
  list-style: none;
  margin: 0;
  padding: 0;
  font-size: 13px;
}
.phases li {
  display: flex;
  align-items: center;
  gap: 6px;
}
.phase-done {
  color: var(--success-600, var(--text-primary));
}
.phase-current {
  color: var(--text-primary);
  font-weight: 600;
}
.phase-todo {
  color: var(--text-muted);
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
  word-break: break-all;
}
.notes {
  margin: 0;
  padding-left: 18px;
  color: var(--text-secondary, var(--text-muted));
  font-size: 0.9em;
}
.notes li {
  margin-bottom: 4px;
}
.source-state {
  display: flex;
  gap: 8px;
}
.btn {
  align-self: flex-start;
}
</style>
