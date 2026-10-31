<script setup lang="ts">
import { ref, watch, computed, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { versioningApi } from '@/api/versioning'
import type { VersionPlan, VersionSettings } from '@/api/types'

// Edits a version policy's settings and previews the tags they produce. Shared by the
// create-pipeline modal and the policy page, so both explain the rules the same way.
const props = defineProps<{
  modelValue: VersionSettings
  readonly?: boolean
  // Preview on top of an existing history, so the next tag reflects what was already built.
  policy?: string
  pipeline?: string
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: VersionSettings): void }>()

const { t } = useI18n()
const { currentWorkspaceId } = storeToRefs(useWorkspaceStore())

const s = computed(() => props.modelValue)
function patch(p: Partial<VersionSettings>) {
  emit('update:modelValue', { ...props.modelValue, ...p })
}
function patchBranches(p: Partial<VersionSettings['branches']>) {
  patch({ branches: { ...props.modelValue.branches, ...p } })
}

// A first version off the new series would be refused, so it is cleared and the server derives it
// (2.x.x opens at 2.0.0).
function onSeries(series: string) {
  const fixed = series.trim().replace(/^v/, '').split('.')
  const start = (props.modelValue.start ?? '').split('.')
  const fits = fixed.length === 3 && fixed.every((p, i) => p === 'x' || p === start[i])
  patch({ series, start: fits ? props.modelValue.start : '' })
}

// Branch series are a semver idea; carrying them into calver would make the settings invalid.
function onScheme(scheme: VersionSettings['scheme']) {
  if (scheme === 'semver') patch({ scheme })
  else patch({ scheme, branches: { ...props.modelValue.branches, series: {} } })
}

// Lists are edited as comma-separated text and branch series as `pattern = series` lines.
const listText = (v?: string[]) => (v ?? []).join(', ')
const parseList = (v: string) => v.split(',').map((x) => x.trim()).filter(Boolean)
const seriesText = computed(() =>
  Object.entries(s.value.branches.series ?? {}).map(([k, v]) => `${k} = ${v}`).join('\n'),
)
function setSeriesText(v: string) {
  const out: Record<string, string> = {}
  for (const line of v.split('\n')) {
    const [k, val] = line.split('=').map((x) => x?.trim())
    if (k && val) out[k] = val
  }
  patchBranches({ series: out })
}

const preview = ref<VersionPlan[]>([])
const branchPreview = ref<VersionPlan | null>(null)
const previewError = ref('')
let timer: ReturnType<typeof setTimeout> | undefined

async function loadPreview() {
  const wid = currentWorkspaceId.value
  if (!wid || s.value.scheme === 'run') { preview.value = []; branchPreview.value = null; previewError.value = ''; return }
  try {
    const body = { settings: s.value, policy: props.policy, pipeline: props.pipeline }
    preview.value = (await versioningApi.preview(wid, { ...body, count: 4 })).data.data ?? []
    branchPreview.value = s.value.branches.others === 'prerelease' && (s.value.branches.release?.length ?? 0) > 0
      ? (await versioningApi.preview(wid, { ...body, branch: 'feat/login', count: 1 })).data.data?.[0] ?? null
      : null
    previewError.value = ''
  } catch (e: unknown) {
    const err = e as { response?: { data?: { error?: { message?: string } } } }
    previewError.value = err.response?.data?.error?.message ?? t('versioning.previewFailed')
    preview.value = []
    branchPreview.value = null
  }
}
watch(() => [props.modelValue, props.policy, props.pipeline], () => {
  clearTimeout(timer)
  timer = setTimeout(loadPreview, 300)
}, { deep: true, immediate: true })
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <div class="versioning-form">
    <div class="form-row">
      <div class="form-group">
        <label class="form-label">{{ $t('versioning.scheme') }}</label>
        <select class="form-select" :value="s.scheme" :disabled="readonly" :aria-label="$t('versioning.scheme')"
          @change="onScheme(($event.target as HTMLSelectElement).value as VersionSettings['scheme'])">
          <option value="semver">{{ $t('versioning.schemes.semver') }}</option>
          <option value="calver">{{ $t('versioning.schemes.calver') }}</option>
          <option value="run">{{ $t('versioning.schemes.run') }}</option>
        </select>
      </div>
      <div v-if="s.scheme !== 'run'" class="form-group">
        <label class="form-label">{{ $t('versioning.template') }}</label>
        <input class="form-input mono" :value="s.template" placeholder="v{version}" :disabled="readonly" :aria-label="$t('versioning.template')"
          @input="patch({ template: ($event.target as HTMLInputElement).value })" />
        <p class="form-hint">{{ $t('versioning.templateHint') }}</p>
      </div>
    </div>

    <p v-if="s.scheme === 'run'" class="form-hint">{{ $t('versioning.runHint') }}</p>

    <template v-if="s.scheme === 'semver'">
      <div class="form-row three">
        <div class="form-group">
          <label class="form-label">{{ $t('versioning.series') }}</label>
          <input class="form-input mono" :value="s.series" placeholder="1.x.x" :disabled="readonly" :aria-label="$t('versioning.series')"
            @change="onSeries(($event.target as HTMLInputElement).value)" />
        </div>
        <div class="form-group">
          <label class="form-label">{{ $t('versioning.start') }}</label>
          <input class="form-input mono" :value="s.start" placeholder="1.0.0" :disabled="readonly" :aria-label="$t('versioning.start')"
            @input="patch({ start: ($event.target as HTMLInputElement).value })" />
        </div>
        <div class="form-group">
          <label class="form-label">{{ $t('versioning.bump') }}</label>
          <select class="form-select" :value="s.bump" :disabled="readonly" :aria-label="$t('versioning.bump')"
            @change="patch({ bump: ($event.target as HTMLSelectElement).value as VersionSettings['bump'] })">
            <option value="patch">{{ $t('versioning.bumps.patch') }}</option>
            <option value="minor">{{ $t('versioning.bumps.minor') }}</option>
            <option value="major">{{ $t('versioning.bumps.major') }}</option>
          </select>
        </div>
      </div>
      <p class="form-hint">{{ $t('versioning.seriesHint') }}</p>
      <label class="check">
        <input type="checkbox" :checked="s.bump_from_commits" :disabled="readonly"
          @change="patch({ bump_from_commits: ($event.target as HTMLInputElement).checked })" />
        <span>{{ $t('versioning.bumpFromCommits') }}</span>
      </label>
    </template>

    <div v-if="s.scheme === 'calver'" class="form-group">
      <label class="form-label">{{ $t('versioning.calverFormat') }}</label>
      <select class="form-select" :value="s.calver_format || 'YYYY.MM'" :disabled="readonly" :aria-label="$t('versioning.calverFormat')"
        @change="patch({ calver_format: ($event.target as HTMLSelectElement).value })">
        <option v-for="f in ['YYYY.MM', 'YYYY.0M', 'YY.MM', 'YYYY.MM.DD']" :key="f" :value="f">{{ f }}.N</option>
      </select>
    </div>

    <details v-if="s.scheme !== 'run'" class="advanced">
      <summary>{{ $t('versioning.branchesAndTags') }}</summary>
      <div class="form-row">
        <div class="form-group">
          <label class="form-label">{{ $t('versioning.releaseBranches') }}</label>
          <input class="form-input mono" :value="listText(s.branches.release)" placeholder="main" :disabled="readonly" :aria-label="$t('versioning.releaseBranches')"
            @change="patchBranches({ release: parseList(($event.target as HTMLInputElement).value) })" />
          <p class="form-hint">{{ $t('versioning.releaseBranchesHint') }}</p>
        </div>
        <div class="form-group">
          <label class="form-label">{{ $t('versioning.otherBranches') }}</label>
          <select class="form-select" :value="s.branches.others || 'prerelease'" :disabled="readonly" :aria-label="$t('versioning.otherBranches')"
            @change="patchBranches({ others: ($event.target as HTMLSelectElement).value as VersionSettings['branches']['others'] })">
            <option value="prerelease">{{ $t('versioning.others.prerelease') }}</option>
            <option value="run-tag">{{ $t('versioning.others.runTag') }}</option>
          </select>
        </div>
      </div>
      <div class="form-group">
        <label class="form-label">{{ $t('versioning.floatingTags') }}</label>
        <input class="form-input mono" :value="listText(s.floating_tags)" placeholder="latest, v{major}" :disabled="readonly" :aria-label="$t('versioning.floatingTags')"
          @change="patch({ floating_tags: parseList(($event.target as HTMLInputElement).value) })" />
        <p class="form-hint">{{ $t('versioning.floatingTagsHint') }}</p>
      </div>
      <div v-if="s.scheme === 'semver'" class="form-group" style="margin-bottom: 0">
        <label class="form-label">{{ $t('versioning.branchSeries') }}</label>
        <textarea class="form-textarea mono" rows="2" :value="seriesText" placeholder="release/1.* = 1.x.x" :disabled="readonly" :aria-label="$t('versioning.branchSeries')"
          @change="setSeriesText(($event.target as HTMLTextAreaElement).value)"></textarea>
        <p class="form-hint">{{ $t('versioning.branchSeriesHint') }}</p>
      </div>
    </details>

    <div v-if="s.scheme !== 'run'" class="preview" aria-live="polite">
      <span class="preview-label">{{ $t('versioning.nextTags') }}</span>
      <span v-if="previewError" class="preview-error"><span class="mdi mdi-alert-circle-outline"></span> {{ previewError }}</span>
      <template v-else>
        <code v-for="(p, i) in preview" :key="p.tag" class="tag-chip" :class="{ first: i === 0 }">{{ p.tag }}</code>
        <span v-if="branchPreview" class="preview-branch">
          {{ $t('versioning.onBranch', { branch: 'feat/login' }) }} <code class="tag-chip">{{ branchPreview.tag }}</code>
        </span>
      </template>
    </div>
  </div>
</template>

<style scoped>
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.form-row.three { grid-template-columns: 1fr 1fr 1fr; }
@media (max-width: 640px) { .form-row, .form-row.three { grid-template-columns: 1fr; } }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.check { display: flex; align-items: center; gap: 8px; font-size: 13px; margin: 6px 0 12px; cursor: pointer; }
.advanced { margin: 4px 0 12px; }
.advanced summary { cursor: pointer; font-size: 13px; font-weight: 500; color: var(--text-secondary, var(--text-muted)); margin-bottom: 10px; }
.preview {
  display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 10px 12px;
  border: 1px dashed var(--border); border-radius: 8px; font-size: 12px;
}
.preview-label { color: var(--text-muted); margin-right: 4px; }
.preview-error { color: var(--danger-600, #dc2626); }
.preview-branch { color: var(--text-muted); margin-left: 8px; }
.tag-chip {
  font-family: 'JetBrains Mono', monospace; font-size: 12px; padding: 1px 7px; border-radius: 4px;
  background: var(--bg-tertiary, rgba(127, 127, 127, 0.12));
}
.tag-chip.first { background: var(--primary-50, rgba(99, 102, 241, 0.12)); color: var(--primary-600, inherit); font-weight: 600; }
</style>
