<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  registryApi,
  type RegistryInfo,
  type RegistryRepositoryOverview,
  type RegistryTag,
  type TagDeleteResponse,
} from '@/api/registry'
import { appApi } from '@/api/apps'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { copyText } from '@/utils/clipboard'
import { relativeTime } from '@/utils/time'
import { formatBytes } from '@/utils/bytes'
import type { Application } from '@/api/types'

const route = useRoute()
const { t } = useI18n()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const wsId = computed(() => ws.currentWorkspaceId)
/** The repository name, which may itself contain slashes. */
const repo = computed(() => decodeURIComponent(String(route.params.repo ?? '')))

type Tab = 'overview' | 'tags'
const tabs: { id: Tab; label: string; icon: string }[] = [
  { id: 'overview', label: 'registry.overview', icon: 'mdi-information-outline' },
  { id: 'tags', label: 'registry.tags', icon: 'mdi-tag-multiple-outline' },
]
const activeTab = computed<Tab>(() => (route.query.tab as Tab) || 'overview')
function setTab(t: Tab) {
  router.replace({ query: { ...route.query, tab: t } })
}

const info = ref<RegistryInfo | null>(null)
const overview = ref<RegistryRepositoryOverview | null>(null)
const tags = ref<RegistryTag[]>([])
const apps = ref<Application[]>([])
const loading = ref(true)
const tagsLoading = ref(false)
const notFound = ref(false)

// Without the platform's delete switch every delete is rejected, so the button would only fail.
const canDelete = computed(
  () => ws.currentRole !== null && ws.currentRole !== 'viewer' && info.value?.delete_enabled === true,
)
const deleteDisabledByPlatform = computed(
  () => ws.currentRole !== null && ws.currentRole !== 'viewer' && info.value?.delete_enabled === false,
)

const imagePrefix = computed(() => `${info.value?.image_prefix ?? ''}/${repo.value}`)
function pullCommand(tag: string) {
  return `docker pull ${imagePrefix.value}:${tag}`
}
/** The newest tag's pull command, or '' when the repository has no tags. */
const latestPull = computed(() =>
  overview.value?.latest_tag ? pullCommand(overview.value.latest_tag.name) : '',
)
/** A repository whose last tag was deleted stays in the catalog until garbage collection. */
const recentTags = computed(() => overview.value?.tags ?? [])

const search = ref('')
let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearch() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 250)
}

/** Refreshes the overview without blanking the page (used after a delete). */
async function loadOverview() {
  if (!wsId.value || !repo.value) return
  try {
    const [i, ov] = await Promise.all([
      registryApi.info(wsId.value),
      registryApi.repository(wsId.value, repo.value),
    ])
    info.value = i.data.data
    overview.value = ov.data.data
  } catch (e: any) {
    if (e?.response?.status === 404) notFound.value = true
    else notify.apiError(e)
  }
}

async function load() {
  loading.value = true
  notFound.value = false
  await loadOverview()
  loading.value = false
  // Names for the "built for" link; failure just leaves the app un-named.
  try {
    apps.value = wsId.value ? (await appApi.list(wsId.value)).data.data ?? [] : []
  } catch {
    apps.value = []
  }
}

const { pageable, goToPage } = usePagination(async (page) => {
  // Enriching costs one registry read per tag, so only do it on the Tags tab.
  if (!wsId.value || !repo.value || activeTab.value !== 'tags') return
  tagsLoading.value = true
  try {
    const res = await registryApi.tags(wsId.value, repo.value, page, pageable.value.size, search.value.trim())
    tags.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e: any) {
    if (e?.response?.status !== 404) notify.apiError(e)
    tags.value = []
  } finally {
    tagsLoading.value = false
  }
})

watch([wsId, repo], load, { immediate: true })
watch([activeTab, repo], () => {
  if (activeTab.value === 'tags') goToPage(pageable.value.current_page)
})

function appName(id?: number) {
  if (!id) return null
  return apps.value.find((a) => a.id === id)?.name ?? t('db.appNumber', { id })
}

function shortDigest(d?: string) {
  if (!d) return ''
  return d.startsWith('sha256:') ? d.slice(7, 19) : d.slice(0, 12)
}

async function copy(text: string) {
  if (await copyText(text)) notify.success(t('notify.common.copiedToClipboard'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}

// --- Deletion ---

const selected = ref<Set<string>>(new Set())
const selectable = (tag: RegistryTag) => canDelete.value && !tag.in_use
const pageSelectable = computed(() => tags.value.filter(selectable))
const allOnPageSelected = computed(
  () => pageSelectable.value.length > 0 && pageSelectable.value.every((tag) => selected.value.has(tag.name)),
)
function toggle(name: string, on: boolean) {
  const next = new Set(selected.value)
  if (on) next.add(name)
  else next.delete(name)
  selected.value = next
}
function toggleAll(on: boolean) {
  const next = new Set(selected.value)
  for (const tag of pageSelectable.value) {
    if (on) next.add(tag.name)
    else next.delete(tag.name)
  }
  selected.value = next
}
watch(repo, () => { selected.value = new Set() })

// The registry deletes images, not tags, so tags sharing one are shown together before anyone deletes.
const tagsByDigest = computed(() => {
  const by = new Map<string, string[]>()
  for (const tag of tags.value) {
    if (!tag.digest) continue
    by.set(tag.digest, [...(by.get(tag.digest) ?? []), tag.name])
  }
  return by
})
function sameImageAs(tag: RegistryTag): string[] {
  return tag.digest ? (tagsByDigest.value.get(tag.digest) ?? []).filter((n) => n !== tag.name) : []
}

const preview = ref<TagDeleteResponse | null>(null)
const previewing = ref(false)
const deleting = ref(false)
const willDelete = computed(() => preview.value?.tags.filter((o) => o.result === 'would_delete') ?? [])
const leftAlone = computed(() => preview.value?.tags.filter((o) => o.result !== 'would_delete') ?? [])
const extraTags = computed(() => {
  const named = new Set(willDelete.value.map((o) => o.tag))
  return [...new Set(willDelete.value.flatMap((o) => o.siblings ?? []))].filter((n) => !named.has(n))
})

function outcomeLabel(result: string) {
  return t(`registry.outcome.${result}`)
}

// Every delete is previewed first, so a tag that goes with another is named before it is gone.
async function askDelete(names: string[]) {
  if (!wsId.value || names.length === 0) return
  previewing.value = true
  try {
    const res = (await registryApi.deleteTags(wsId.value, repo.value, names, true)).data.data
    if (!res.tags.some((o) => o.result === 'would_delete')) {
      notify.error(t('registry.nothingToDelete', { tags: res.tags.map((o) => `${o.tag} (${outcomeLabel(o.result)})`).join(', ') }))
      return
    }
    preview.value = res
  } catch (e) {
    notify.apiError(e)
  } finally {
    previewing.value = false
  }
}

// --- Select by rule ---

const ruleOpen = ref(false)
const ruleBusy = ref(false)
const rule = ref({ keepOn: true, keepNewest: 10, ageOn: false, olderThanDays: 30 })
const ruleValid = computed(
  () => (rule.value.keepOn && rule.value.keepNewest > 0) || (rule.value.ageOn && rule.value.olderThanDays > 0),
)

// It only pre-checks the boxes; the delete that follows is previewed like any other.
async function applyRule() {
  if (!wsId.value || !ruleValid.value) return
  ruleBusy.value = true
  try {
    const sel = (await registryApi.selectTags(wsId.value, repo.value, {
      keep_newest: rule.value.keepOn ? rule.value.keepNewest : undefined,
      older_than_days: rule.value.ageOn ? rule.value.olderThanDays : undefined,
    })).data.data
    selected.value = new Set(sel.tags)
    ruleOpen.value = false
    const notes: string[] = []
    if (sel.in_use.length) notes.push(t('registry.ruleInUse', { n: sel.in_use.length }, sel.in_use.length))
    if (sel.unknown_age.length) notes.push(t('registry.ruleUnknownAge', { n: sel.unknown_age.length }, sel.unknown_age.length))
    if (sel.truncated) notes.push(t('registry.ruleTruncated', { n: sel.matched }))
    const head = t('registry.ruleSelected', { n: sel.tags.length }, sel.tags.length)
    notify.success(notes.length ? `${head} ${notes.join(' ')}` : head)
  } catch (e) {
    notify.apiError(e)
  } finally {
    ruleBusy.value = false
  }
}

// --- Delete repository ---

const canDeleteRepo = computed(() => ws.isWorkspaceAdmin && info.value?.delete_enabled === true)
const repoDeleteOpen = ref(false)
const repoDeleteTyped = ref('')
const repoDeleting = ref(false)

async function deleteRepository() {
  if (!wsId.value || repoDeleteTyped.value !== repo.value) return
  repoDeleting.value = true
  try {
    const res = (await registryApi.deleteRepository(wsId.value, repo.value)).data.data
    notify.success(t('notify.registry.repositoryDeleted', { repo: repo.value, size: formatBytes(res.freed_bytes) }))
    repoDeleteOpen.value = false
    router.push({ name: 'registry' })
  } catch (e) {
    notify.apiError(e)
  } finally {
    repoDeleting.value = false
  }
}

async function confirmDelete() {
  if (!wsId.value || willDelete.value.length === 0) return
  deleting.value = true
  try {
    const res = (await registryApi.deleteTags(wsId.value, repo.value, willDelete.value.map((o) => o.tag), false)).data.data
    if (res.removed.length) {
      notify.success(t('notify.registry.tagsDeleted', { n: res.removed.length, size: formatBytes(res.freed_bytes) }, res.removed.length))
    }
    const missed = res.tags.filter((o) => o.result !== 'deleted')
    if (missed.length) {
      notify.error(t('notify.registry.tagsNotDeleted', { tags: missed.map((o) => `${o.tag} (${outcomeLabel(o.result)})`).join(', ') }))
    }
    const gone = new Set(res.removed)
    selected.value = new Set([...selected.value].filter((n) => !gone.has(n)))
    preview.value = null
    // The newest tag may have changed; refresh quietly without blanking the page.
    await Promise.all([loadOverview(), goToPage(pageable.value.current_page)])
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div class="heading">
        <RouterLink :to="{ name: 'registry' }" class="back" :aria-label="$t('registry.backToRegistry')">
          <span class="mdi mdi-arrow-left"></span>
        </RouterLink>
        <div>
          <h1>{{ repo }}</h1>
          <code v-if="info?.image_prefix" class="subtitle mono">{{ imagePrefix }}</code>
        </div>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <div v-else-if="notFound" class="card">
      <div class="card-body empty">
        <span class="mdi mdi-package-variant-closed-remove empty-icon"></span>
        <div>
          <p class="empty-title">{{ $t('registry.noSuchImage') }}</p>
          <p class="text-muted text-sm">
            <i18n-t keypath="registry.noSuchImageHint" tag="span"><template #repo><code class="mono">{{ repo }}</code></template></i18n-t>
            <RouterLink :to="{ name: 'registry' }">{{ $t('registry.backToRegistry') }}</RouterLink>
          </p>
        </div>
      </div>
    </div>

    <template v-else-if="overview">
      <div class="tabs">
        <button v-for="t in tabs" :key="t.id" class="tab" :class="{ active: activeTab === t.id }" @click="setTab(t.id)">
          <span class="mdi" :class="t.icon"></span> {{ $t(t.label) }}
          <span v-if="t.id === 'tags'" class="tab-count">{{ overview.tag_count }}</span>
        </button>
      </div>

      <!-- Overview -->
      <template v-if="activeTab === 'overview'">
        <div class="card mb-4">
          <div class="card-header"><h2>{{ $t('registry.newestTag') }}</h2></div>
          <div v-if="overview.latest_tag" class="card-body">
            <div class="details-grid">
              <div class="detail">
                <span class="detail-label">{{ $t('registry.tag') }}</span>
                <span class="detail-value">
                  <code class="mono">{{ overview.latest_tag.name }}</code>
                  <span v-if="overview.latest_tag.in_use" class="badge badge-success">{{ $t('registry.inUse') }}</span>
                </span>
              </div>
              <div class="detail">
                <span class="detail-label">{{ $t('registry.size') }}</span>
                <span class="detail-value">{{ formatBytes(overview.latest_tag.size_bytes) }}</span>
              </div>
              <div class="detail">
                <span class="detail-label">{{ $t('registry.digest') }}</span>
                <code class="mono" :title="overview.latest_tag.digest">{{ shortDigest(overview.latest_tag.digest) || '—' }}</code>
              </div>
              <div v-if="overview.latest_tag.built_at" class="detail">
                <span class="detail-label">{{ $t('registry.built') }}</span>
                <span class="detail-value" :title="new Date(overview.latest_tag.built_at).toLocaleString()">
                  {{ relativeTime(overview.latest_tag.built_at) }}
                </span>
              </div>
              <div v-if="overview.latest_tag.commit" class="detail">
                <span class="detail-label">{{ $t('registry.commit') }}</span>
                <code class="mono">{{ overview.latest_tag.commit.slice(0, 12) }}</code>
              </div>
              <div v-if="appName(overview.latest_tag.application_id)" class="detail">
                <span class="detail-label">{{ $t('registry.builtFor') }}</span>
                <RouterLink :to="{ name: 'app-detail', params: { id: overview.latest_tag.application_id } }">
                  {{ appName(overview.latest_tag.application_id) }}
                </RouterLink>
              </div>
            </div>
            <div class="snippet">
              <code>{{ latestPull }}</code>
              <button class="btn-icon btn-icon-muted" :title="$t('registry.copy')" :aria-label="$t('registry.copy')" @click="copy(latestPull)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
            <p v-if="!overview.latest_tag.built_at" class="text-muted text-sm hint">{{ $t('registry.noBuildRecord') }}</p>
          </div>
          <div v-else class="card-body empty">
            <span class="mdi mdi-tag-off-outline empty-icon"></span>
            <div>
              <p class="empty-title">{{ $t('registry.noTags') }}</p>
              <p class="text-muted text-sm">{{ $t('registry.noTagsHint') }}</p>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header"><h2>{{ $t('registry.recentTags') }}</h2></div>
          <div class="card-body">
            <div v-if="recentTags.length" class="tag-grid">
              <code v-for="t in recentTags" :key="t" class="tag">{{ t }}</code>
              <button v-if="overview.tag_count > recentTags.length" class="link-btn" @click="setTab('tags')">
                {{ $t('registry.seeAllTags', { n: overview.tag_count }) }}
              </button>
            </div>
            <p v-else class="text-muted text-sm" style="margin: 0">{{ $t('registry.noTagsDot') }}</p>
          </div>
        </div>

        <div v-if="canDeleteRepo" class="card danger-card mt-4">
          <div class="card-body danger-row">
            <div>
              <h3 class="danger-title">{{ $t('registry.deleteRepository') }}</h3>
              <p class="text-muted text-sm">{{ $t('registry.deleteRepositoryHint') }}</p>
            </div>
            <button class="btn btn-danger btn-sm" @click="repoDeleteTyped = ''; repoDeleteOpen = true">
              <span class="mdi mdi-trash-can-outline"></span>{{ $t('registry.deleteRepository') }}</button>
          </div>
        </div>
      </template>

      <!-- Tags -->
      <template v-else>
        <div class="card">
          <div class="card-header repos-header">
            <div>
              <h2>{{ $t('registry.tags') }}</h2>
              <p class="text-muted text-sm" style="margin: 2px 0 0">
                {{ search ? $t('registry.tagCountMatching', pageable.total_elements) : $t('registry.tagCount', pageable.total_elements) }}
                <template v-if="deleteDisabledByPlatform">{{ $t('registry.deletionDisabled') }}</template>
              </p>
            </div>
            <div class="repos-actions">
              <button v-if="canDelete" class="btn btn-secondary btn-sm" @click="ruleOpen = true">
                <span class="mdi mdi-filter-variant"></span>{{ $t('registry.selectByRule') }}</button>
              <div class="search">
                <span class="mdi mdi-magnify"></span>
                <input v-model="search" class="form-input" type="search" :placeholder="$t('registry.filterTags')" :aria-label="$t('registry.filterTags')" @input="onSearch" />
              </div>
              <button class="btn btn-secondary btn-sm" :disabled="tagsLoading" @click="goToPage(pageable.current_page)">
                <span class="mdi mdi-refresh" :class="{ 'mdi-spin': tagsLoading }"></span>{{ $t('volumes.refresh') }}</button>
            </div>
          </div>

          <div v-if="tagsLoading && tags.length === 0" class="card-body"><span class="spinner"></span></div>

          <div v-else-if="tags.length === 0" class="card-body empty">
            <span class="mdi mdi-tag-off-outline empty-icon"></span>
            <div>
              <p class="empty-title">{{ search ? $t('registry.noTagMatch', { query: search }) : $t('registry.noTags') }}</p>
              <p v-if="search" class="text-muted text-sm">
                <button class="link-btn" @click="search = ''; goToPage(0)">{{ $t('registry.clearTheFilter') }}</button>
              </p>
            </div>
          </div>

          <div v-if="selected.size > 0" class="selection-bar">
            <span>{{ $t('registry.selectedCount', { n: selected.size }, selected.size) }}</span>
            <button class="btn btn-danger btn-sm" :disabled="previewing" @click="askDelete([...selected])">
              <span class="mdi" :class="previewing ? 'mdi-loading mdi-spin' : 'mdi-trash-can-outline'"></span>{{ $t('registry.deleteSelected') }}</button>
            <button class="btn btn-ghost btn-sm" @click="selected = new Set()">{{ $t('registry.clearSelection') }}</button>
          </div>

          <div v-if="tags.length > 0" class="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th v-if="canDelete" class="check-col">
                    <input
                      type="checkbox"
                      :checked="allOnPageSelected"
                      :disabled="pageSelectable.length === 0"
                      :aria-label="$t('registry.selectAllOnPage')"
                      :title="$t('registry.selectAllOnPage')"
                      @change="toggleAll(($event.target as HTMLInputElement).checked)"
                    />
                  </th>
                  <th>{{ $t('registry.tag') }}</th>
                  <th>{{ $t('registry.size') }}</th>
                  <th>{{ $t('registry.digest') }}</th>
                  <th>{{ $t('registry.built') }}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="t in tags" :key="t.name" :class="{ busy: deleting && selected.has(t.name) }">
                  <td v-if="canDelete" class="check-col">
                    <input
                      type="checkbox"
                      :checked="selected.has(t.name)"
                      :disabled="!selectable(t)"
                      :aria-label="$t('registry.selectTag', { tag: t.name })"
                      :title="t.in_use ? $t('registry.inUseHint') : ''"
                      @change="toggle(t.name, ($event.target as HTMLInputElement).checked)"
                    />
                  </td>
                  <td>
                    <div class="tag-cell">
                      <code class="mono">{{ t.name }}</code>
                      <span v-if="t.in_use" class="badge badge-success" :title="$t('registry.inUseHint')">{{ $t('registry.inUse') }}</span>
                    </div>
                    <span v-if="sameImageAs(t).length" class="cell-sub">
                      <span class="mdi mdi-link-variant"></span> {{ $t('registry.sameImageAs', { tags: sameImageAs(t).join(', ') }) }}
                    </span>
                    <span v-if="appName(t.application_id)" class="cell-sub">
                      {{ $t('registry.builtForApp', { app: appName(t.application_id) }) }}
                      <template v-if="t.commit"> · {{ t.commit.slice(0, 8) }}</template>
                    </span>
                  </td>
                  <td>{{ formatBytes(t.size_bytes) }}</td>
                  <td><code class="mono" :title="t.digest">{{ shortDigest(t.digest) || '—' }}</code></td>
                  <td>
                    <span v-if="t.built_at" :title="new Date(t.built_at).toLocaleString()">{{ relativeTime(t.built_at) }}</span>
                    <span v-else class="text-muted">—</span>
                  </td>
                  <td class="text-right table-actions">
                    <button class="btn-icon btn-icon-muted" :title="$t('registry.copyDockerPullCommand')" :aria-label="$t('registry.copyDockerPullCommand')" @click="copy(pullCommand(t.name))">
                      <span class="mdi mdi-content-copy"></span>
                    </button>
                    <button
                      v-if="canDelete"
                      class="btn-icon btn-icon-danger"
                      :disabled="t.in_use || previewing || deleting"
                      :title="t.in_use ? $t('registry.inUseHint') : $t('confirm.title.deleteTag')"
                      :aria-label="$t('confirm.title.deleteTag')"
                      @click="askDelete([t.name])"
                    >
                      <span class="mdi mdi-trash-can-outline"></span>
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <Pagination :pageable="pageable" @page="goToPage" />
      </template>
    </template>

    <ConfirmDialog
      :open="ruleOpen"
      :title="$t('registry.selectByRule')"
      :message="$t('registry.ruleHint')"
      :confirm-label="$t('registry.ruleApply')"
      variant="primary"
      :busy="ruleBusy"
      :confirm-disabled="!ruleValid"
      @confirm="applyRule"
      @cancel="ruleOpen = false"
    >
      <div class="rule-form">
        <label class="checkbox-label">
          <input v-model="rule.keepOn" type="checkbox" />
          <i18n-t keypath="registry.ruleKeepNewest" tag="span">
            <template #n><input v-model.number="rule.keepNewest" type="number" min="1" class="form-input rule-num" :disabled="!rule.keepOn" :aria-label="$t('registry.ruleKeepNewestLabel')" /></template>
          </i18n-t>
        </label>
        <label class="checkbox-label">
          <input v-model="rule.ageOn" type="checkbox" />
          <i18n-t keypath="registry.ruleOlderThan" tag="span">
            <template #n><input v-model.number="rule.olderThanDays" type="number" min="1" class="form-input rule-num" :disabled="!rule.ageOn" :aria-label="$t('registry.ruleOlderThanLabel')" /></template>
          </i18n-t>
        </label>
        <p class="text-muted text-sm">{{ $t('registry.ruleNever') }}</p>
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :open="repoDeleteOpen"
      :title="$t('registry.deleteRepository')"
      :message="$t('registry.deleteRepositoryConfirm', { repo })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="repoDeleting"
      :confirm-disabled="repoDeleteTyped !== repo"
      @confirm="deleteRepository"
      @cancel="repoDeleteOpen = false"
    >
      <input v-model="repoDeleteTyped" class="form-input mono" :placeholder="repo" :aria-label="$t('registry.typeRepositoryName')" style="margin-top: 12px" />
    </ConfirmDialog>

    <ConfirmDialog
      :open="!!preview"
      :title="$t('confirm.title.deleteTags', { n: willDelete.length }, willDelete.length)"
      :message="$t('confirm.message.registry.deleteTags', { n: willDelete.length, repo, tags: willDelete.map((o) => o.tag).join(', ') }, willDelete.length)"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="preview = null"
    >
      <div v-if="extraTags.length" class="preview-block preview-warn">
        <span class="mdi mdi-alert-outline"></span>
        <div>
          <p>{{ $t('registry.deletePreviewSiblings') }}</p>
          <div class="tag-grid"><code v-for="n in extraTags" :key="n" class="tag">{{ n }}</code></div>
        </div>
      </div>
      <div v-if="leftAlone.length" class="preview-block">
        <span class="mdi mdi-information-outline"></span>
        <div>
          <p>{{ $t('registry.deletePreviewSkipped') }}</p>
          <p class="text-muted text-sm">{{ leftAlone.map((o) => `${o.tag} (${outcomeLabel(o.result)})`).join(', ') }}</p>
        </div>
      </div>
      <p v-if="preview && preview.freed_bytes > 0" class="text-muted text-sm preview-freed">
        {{ $t('registry.deletePreviewFreed', { size: formatBytes(preview.freed_bytes) }) }}
      </p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.page-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.heading { display: flex; align-items: center; gap: 12px; min-width: 0; }
.back { display: inline-flex; align-items: center; color: var(--text-muted); font-size: 20px; }
.back:hover { color: var(--text-primary); }
.subtitle { font-size: 12px; color: var(--text-muted); }
.tab-count {
  margin-left: 6px; padding: 0 6px; border-radius: 10px; font-size: 11px;
  background: var(--bg-tertiary); color: var(--text-secondary);
}
.details-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 16px; }
.detail { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.detail-label { font-size: 12px; color: var(--text-muted); }
.detail-value { display: flex; align-items: center; gap: 8px; font-size: 14px; }
.snippet {
  display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 16px;
  background: var(--bg-tertiary); border-radius: var(--radius); padding: 8px 12px;
  font-family: monospace; font-size: 13px; overflow-x: auto;
}
.snippet code { white-space: nowrap; }
.hint { margin: 12px 0 0; }
.repos-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.repos-actions { display: flex; align-items: center; gap: 8px; }
.search { position: relative; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; width: 240px; max-width: 100%; }
.tag-grid { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.tag {
  padding: 3px 10px; font-size: 12px; color: var(--text-primary);
  background: var(--bg-tertiary); border: 1px solid var(--border-primary); border-radius: var(--radius);
}
.tag-cell { display: flex; align-items: center; gap: 8px; }
.cell-sub { display: block; font-size: 12px; color: var(--text-muted); margin-top: 2px; }
.link-btn { background: none; border: 0; padding: 0; font: inherit; color: var(--primary-500); cursor: pointer; }
.link-btn:hover { text-decoration: underline; }
tr.busy { opacity: 0.5; }
.check-col { width: 32px; }
.selection-bar {
  display: flex; align-items: center; gap: 8px; padding: 8px 16px;
  background: var(--bg-tertiary); border-bottom: 1px solid var(--border-primary); font-size: 13px;
}
.selection-bar > span { margin-right: auto; }
.preview-block { display: flex; gap: 8px; margin-top: 12px; font-size: 13px; }
.preview-block p { margin: 0 0 6px; }
.preview-block > .mdi { font-size: 18px; color: var(--text-muted); }
.preview-warn > .mdi { color: var(--warning-600); }
.preview-freed { margin: 12px 0 0; }
.rule-form { display: flex; flex-direction: column; gap: 10px; margin-top: 12px; }
.rule-form .checkbox-label { display: flex; align-items: center; gap: 8px; }
.rule-form p { margin: 0; }
.rule-num { width: 72px; display: inline-block; margin: 0 4px; padding: 4px 8px; }
.danger-card { border-color: var(--danger-200); }
.danger-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.danger-title { margin: 0 0 4px; font-size: 14px; }
.danger-row p { margin: 0; }
.empty { display: flex; align-items: center; gap: 16px; }
.empty-icon { font-size: 36px; color: var(--text-muted); flex-shrink: 0; }
.empty-title { font-weight: 600; color: var(--text-primary); margin: 0 0 2px; }
.mono { font-family: monospace; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
.mdi-spin { animation: mdi-spin 0.9s linear infinite; }
@keyframes mdi-spin { to { transform: rotate(360deg); } }
</style>
