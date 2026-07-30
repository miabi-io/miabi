<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  registryApi,
  type RegistryInfo,
  type RegistryRepositoryOverview,
  type RegistryTag,
} from '@/api/registry'
import { appApi } from '@/api/apps'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { copyText } from '@/utils/clipboard'
import { relativeTime } from '@/utils/time'
import type { Application } from '@/api/types'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const wsId = computed(() => ws.currentWorkspaceId)
/** The repository name, which may itself contain slashes. */
const repo = computed(() => decodeURIComponent(String(route.params.repo ?? '')))

type Tab = 'overview' | 'tags'
const tabs: { id: Tab; label: string; icon: string }[] = [
  { id: 'overview', label: 'Overview', icon: 'mdi-information-outline' },
  { id: 'tags', label: 'Tags', icon: 'mdi-tag-multiple-outline' },
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

// Deleting needs both the role and the platform's registry delete switch —
// without the switch every delete is rejected, so offering the button would only
// produce a confusing failure.
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
  // Enriching a page of tags costs one registry read per tag, so it only happens
  // when the Tags tab is actually open — landing on Overview must not pay for it.
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
// Load the tags the first time the tab is opened, and whenever the image changes
// while it's open.
watch([activeTab, repo], () => {
  if (activeTab.value === 'tags') goToPage(pageable.value.current_page)
})

function appName(id?: number) {
  if (!id) return null
  return apps.value.find((a) => a.id === id)?.name ?? `app #${id}`
}

function shortDigest(d?: string) {
  if (!d) return ''
  return d.startsWith('sha256:') ? d.slice(7, 19) : d.slice(0, 12)
}

function formatBytes(n?: number) {
  if (!n) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let u = 0
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024
    u++
  }
  return `${v < 10 && u > 0 ? v.toFixed(1) : Math.round(v)} ${units[u]}`
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied to clipboard')
  else notify.error('Copy failed — select and copy it manually')
}

// --- Deletion ---

const confirmTarget = ref<RegistryTag | null>(null)
const deleting = ref<string | null>(null)

async function confirmDelete() {
  if (!confirmTarget.value || !wsId.value) return
  const tag = confirmTarget.value.name
  deleting.value = tag
  try {
    await registryApi.deleteTag(wsId.value, repo.value, tag)
    notify.success(`Deleted ${repo.value}:${tag}`)
    confirmTarget.value = null
    // The newest tag may have changed, so refresh the overview too — quietly,
    // without blanking the page the user is looking at.
    await Promise.all([loadOverview(), goToPage(pageable.value.current_page)])
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = null
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div class="heading">
        <RouterLink :to="{ name: 'registry' }" class="back" aria-label="Back to the registry">
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
          <p class="empty-title">No such image</p>
          <p class="text-muted text-sm">
            <code class="mono">{{ repo }}</code> isn't in this workspace's registry — it may have been deleted.
            <RouterLink :to="{ name: 'registry' }">Back to the registry</RouterLink>
          </p>
        </div>
      </div>
    </div>

    <template v-else-if="overview">
      <div class="tabs">
        <button v-for="t in tabs" :key="t.id" class="tab" :class="{ active: activeTab === t.id }" @click="setTab(t.id)">
          <span class="mdi" :class="t.icon"></span> {{ t.label }}
          <span v-if="t.id === 'tags'" class="tab-count">{{ overview.tag_count }}</span>
        </button>
      </div>

      <!-- Overview -->
      <template v-if="activeTab === 'overview'">
        <div class="card mb-4">
          <div class="card-header"><h2>Newest tag</h2></div>
          <div v-if="overview.latest_tag" class="card-body">
            <div class="details-grid">
              <div class="detail">
                <span class="detail-label">Tag</span>
                <span class="detail-value">
                  <code class="mono">{{ overview.latest_tag.name }}</code>
                  <span v-if="overview.latest_tag.in_use" class="badge badge-success">in use</span>
                </span>
              </div>
              <div class="detail">
                <span class="detail-label">Size</span>
                <span class="detail-value">{{ formatBytes(overview.latest_tag.size_bytes) }}</span>
              </div>
              <div class="detail">
                <span class="detail-label">Digest</span>
                <code class="mono" :title="overview.latest_tag.digest">{{ shortDigest(overview.latest_tag.digest) || '—' }}</code>
              </div>
              <div v-if="overview.latest_tag.built_at" class="detail">
                <span class="detail-label">Built</span>
                <span class="detail-value" :title="new Date(overview.latest_tag.built_at).toLocaleString()">
                  {{ relativeTime(overview.latest_tag.built_at) }}
                </span>
              </div>
              <div v-if="overview.latest_tag.commit" class="detail">
                <span class="detail-label">Commit</span>
                <code class="mono">{{ overview.latest_tag.commit.slice(0, 12) }}</code>
              </div>
              <div v-if="appName(overview.latest_tag.application_id)" class="detail">
                <span class="detail-label">Built for</span>
                <RouterLink :to="{ name: 'app-detail', params: { id: overview.latest_tag.application_id } }">
                  {{ appName(overview.latest_tag.application_id) }}
                </RouterLink>
              </div>
            </div>
            <div class="snippet">
              <code>{{ latestPull }}</code>
              <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(latestPull)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
            <p v-if="!overview.latest_tag.built_at" class="text-muted text-sm hint">
              No build record — this image was pushed directly rather than built by a Miabi pipeline, so there's no
              commit or provenance to show.
            </p>
          </div>
          <div v-else class="card-body empty">
            <span class="mdi mdi-tag-off-outline empty-icon"></span>
            <div>
              <p class="empty-title">No tags</p>
              <p class="text-muted text-sm">This repository exists but holds no tags.</p>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header"><h2>Recent tags</h2></div>
          <div class="card-body">
            <div v-if="overview.tags.length" class="tag-grid">
              <code v-for="t in overview.tags" :key="t" class="tag">{{ t }}</code>
              <button v-if="overview.tag_count > overview.tags.length" class="link-btn" @click="setTab('tags')">
                See all {{ overview.tag_count }} tags →
              </button>
            </div>
            <p v-else class="text-muted text-sm" style="margin: 0">No tags.</p>
          </div>
        </div>
      </template>

      <!-- Tags -->
      <template v-else>
        <div class="card">
          <div class="card-header repos-header">
            <div>
              <h2>Tags</h2>
              <p class="text-muted text-sm" style="margin: 2px 0 0">
                {{ pageable.total_elements }}
                {{ pageable.total_elements === 1 ? 'tag' : 'tags' }}{{ search ? ' matching' : '' }}
                <template v-if="deleteDisabledByPlatform"> · deletion is disabled on this platform</template>
              </p>
            </div>
            <div class="repos-actions">
              <div class="search">
                <span class="mdi mdi-magnify"></span>
                <input v-model="search" class="form-input" type="search" placeholder="Filter tags" aria-label="Filter tags" @input="onSearch" />
              </div>
              <button class="btn btn-secondary btn-sm" :disabled="tagsLoading" @click="goToPage(pageable.current_page)">
                <span class="mdi mdi-refresh" :class="{ 'mdi-spin': tagsLoading }"></span> Refresh
              </button>
            </div>
          </div>

          <div v-if="tagsLoading && tags.length === 0" class="card-body"><span class="spinner"></span></div>

          <div v-else-if="tags.length === 0" class="card-body empty">
            <span class="mdi mdi-tag-off-outline empty-icon"></span>
            <div>
              <p class="empty-title">{{ search ? `No tag matches “${search}”` : 'No tags' }}</p>
              <p v-if="search" class="text-muted text-sm">
                <button class="link-btn" @click="search = ''; goToPage(0)">Clear the filter</button>
              </p>
            </div>
          </div>

          <div v-else class="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Tag</th>
                  <th>Size</th>
                  <th>Digest</th>
                  <th>Built</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="t in tags" :key="t.name" :class="{ busy: deleting === t.name }">
                  <td>
                    <div class="tag-cell">
                      <code class="mono">{{ t.name }}</code>
                      <span v-if="t.in_use" class="badge badge-success" title="Held by a live deployment or a pinned release">in use</span>
                    </div>
                    <span v-if="appName(t.application_id)" class="cell-sub">
                      built for {{ appName(t.application_id) }}
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
                    <button class="btn-icon btn-icon-muted" title="Copy docker pull command" aria-label="Copy docker pull command" @click="copy(pullCommand(t.name))">
                      <span class="mdi mdi-content-copy"></span>
                    </button>
                    <button
                      v-if="canDelete"
                      class="btn-icon btn-icon-danger"
                      :disabled="t.in_use || deleting === t.name"
                      :title="t.in_use ? 'In use by a live deployment or pinned release — cannot be deleted' : 'Delete tag'"
                      aria-label="Delete tag"
                      @click="confirmTarget = t"
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
      :open="!!confirmTarget"
      title="Delete tag"
      :message="confirmTarget ? `Delete ${repo}:${confirmTarget.name}? This removes the manifest from the registry and cannot be undone.` : ''"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting !== null"
      @confirm="confirmDelete"
      @cancel="confirmTarget = null"
    />
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
.empty { display: flex; align-items: center; gap: 16px; }
.empty-icon { font-size: 36px; color: var(--text-muted); flex-shrink: 0; }
.empty-title { font-weight: 600; color: var(--text-primary); margin: 0 0 2px; }
.mono { font-family: monospace; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
.mdi-spin { animation: mdi-spin 0.9s linear infinite; }
@keyframes mdi-spin { to { transform: rotate(360deg); } }
</style>
