<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { registryApi, type RegistryInfo, type RegistryRepository } from '@/api/registry'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import { copyText } from '@/utils/clipboard'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

type Tab = 'repositories' | 'connect'
const tabs: { id: Tab; label: string; icon: string }[] = [
  { id: 'repositories', label: 'Repositories', icon: 'mdi-cube-outline' },
  { id: 'connect', label: 'Connect', icon: 'mdi-console-line' },
]
const activeTab = computed<Tab>(() => (route.query.tab as Tab) || 'repositories')
function setTab(t: Tab) {
  router.replace({ query: { ...route.query, tab: t } })
}

const wsId = computed(() => ws.currentWorkspaceId)

const info = ref<RegistryInfo | null>(null)
const repos = ref<RegistryRepository[]>([])
const loading = ref(true)
const reposLoading = ref(false)

// Search filters repository *names*, server-side. Searching within a
// repository's tags belongs on its own page, where the tags are paginated —
// filtering them here would mean reading every tag of every repository.
const search = ref('')
let searchTimer: ReturnType<typeof setTimeout> | undefined

function onSearch() {
  // Each keystroke would otherwise walk the registry catalog.
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 250)
}

function openRepo(name: string) {
  router.push({ name: 'registry-image', params: { repo: name } })
}

async function loadInfo() {
  if (!wsId.value) return
  try {
    info.value = (await registryApi.info(wsId.value)).data.data
  } catch (e) {
    notify.apiError(e)
  }
}

const { pageable, goToPage } = usePagination(async (page) => {
  if (!wsId.value) {
    repos.value = []
    return
  }
  // The pager fires on mount, possibly before the info call lands; the enabled
  // flag gates whether there is anything to list at all.
  if (!info.value) await loadInfo()
  loading.value = false
  if (!info.value?.enabled) {
    repos.value = []
    return
  }
  reposLoading.value = true
  try {
    const res = await registryApi.repositories(wsId.value, page, pageable.value.size, search.value.trim())
    repos.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    reposLoading.value = false
  }
})

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied to clipboard')
  else notify.error('Copy failed — select and copy it manually')
}

watch(wsId, async () => {
  loading.value = true
  search.value = '' // a filter from the previous workspace means nothing here
  info.value = null
  await loadInfo()
  loading.value = false
  await goToPage(0)
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Container Registry</h1>
      <code v-if="info?.enabled" class="host-pill mono">{{ info.host }}</code>
    </div>

    <div v-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <!-- Registry disabled platform-wide -->
    <div v-else-if="info && !info.enabled" class="card">
      <div class="card-body empty">
        <span class="mdi mdi-cube-off-outline empty-icon"></span>
        <div>
          <p class="empty-title">The container registry isn't enabled</p>
          <p class="text-muted text-sm">Ask a platform admin to enable it in <strong>Admin → Container Registry</strong>, then push and pull your images here.</p>
        </div>
      </div>
    </div>

    <template v-else-if="info">
      <div class="tabs">
        <button v-for="t in tabs" :key="t.id" class="tab" :class="{ active: activeTab === t.id }" @click="setTab(t.id)">
          <span class="mdi" :class="t.icon"></span> {{ t.label }}
        </button>
      </div>

      <!-- Repositories -->
      <template v-if="activeTab === 'repositories'">
        <div class="card">
          <div class="card-header repos-header">
            <div>
              <h2>Repositories</h2>
              <p class="text-muted text-sm" style="margin: 2px 0 0">
                {{ pageable.total_elements }}
                {{ pageable.total_elements === 1 ? 'repository' : 'repositories' }}{{ search ? ' matching' : '' }}
              </p>
            </div>
            <div class="repos-actions">
              <div class="search">
                <span class="mdi mdi-magnify"></span>
                <input v-model="search" class="form-input" type="search" placeholder="Filter images" aria-label="Filter images" @input="onSearch" />
              </div>
              <button class="btn btn-secondary btn-sm" :disabled="reposLoading" @click="goToPage(pageable.current_page)">
                <span class="mdi mdi-refresh" :class="{ 'mdi-spin': reposLoading }"></span> Refresh
              </button>
            </div>
          </div>

          <div v-if="reposLoading && repos.length === 0" class="card-body"><span class="spinner"></span></div>

          <div v-else-if="repos.length === 0 && search" class="card-body empty">
            <span class="mdi mdi-magnify empty-icon"></span>
            <div>
              <p class="empty-title">No image matches &ldquo;{{ search }}&rdquo;</p>
              <p class="text-muted text-sm">
                <button class="link-btn" @click="search = ''; goToPage(0)">Clear the filter</button>
              </p>
            </div>
          </div>

          <div v-else-if="repos.length === 0" class="card-body empty">
            <span class="mdi mdi-package-variant empty-icon"></span>
            <div>
              <p class="empty-title">No images yet</p>
              <p class="text-muted text-sm">
                Push your first image &mdash; see the
                <a href="#" @click.prevent="setTab('connect')">Connect</a> tab for the commands.
              </p>
            </div>
          </div>

          <div v-else class="repo-list">
            <div v-for="r in repos" :key="r.name" class="repo">
              <div class="repo-head">
                <span class="mdi mdi-cube-outline repo-icon"></span>
                <button class="repo-name mono link" @click="openRepo(r.name)">{{ info.image_prefix }}/{{ r.name }}</button>
                <span class="badge badge-neutral">{{ r.tag_count }} {{ r.tag_count === 1 ? 'tag' : 'tags' }}</span>
              </div>
              <div v-if="r.tags.length" class="tag-grid">
                <code v-for="t in r.tags" :key="t" class="tag">{{ t }}</code>
                <button v-if="r.tag_count > r.tags.length" class="link-btn more-note" @click="openRepo(r.name)">
                  +{{ r.tag_count - r.tags.length }} more
                </button>
              </div>
              <p v-else class="text-muted text-sm" style="margin: 8px 0 0">No tags.</p>
            </div>
          </div>
        </div>

        <Pagination :pageable="pageable" @page="goToPage" />
      </template>

      <!-- Connect -->
      <template v-else-if="activeTab === 'connect'">
        <div class="card mb-4">
          <div class="card-header"><h2>Connection details</h2></div>
          <div class="card-body details-grid">
            <div class="detail">
              <span class="detail-label">Registry host</span>
              <code class="mono">{{ info.host }}</code>
            </div>
            <div class="detail">
              <span class="detail-label">Your namespace</span>
              <code class="mono">{{ info.namespace }}</code>
            </div>
            <div class="detail">
              <span class="detail-label">Image prefix</span>
              <code class="mono">{{ info.image_prefix }}</code>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header"><h2>Push an image</h2></div>
          <div class="card-body">
            <ol class="steps">
              <li>
                <div class="step-label">1. Log in</div>
                <p class="text-muted text-sm">Use your workspace name (or your username) and a Miabi <router-link to="/api-keys">API token</router-link> as the password.</p>
                <div class="snippet">
                  <code>docker login {{ info.host }} -u {{ info.namespace }} -p &lt;api-token&gt;</code>
                  <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`docker login ${info.host} -u ${info.namespace} -p `)"><span class="mdi mdi-content-copy"></span></button>
                </div>
              </li>
              <li>
                <div class="step-label">2. Tag your image</div>
                <div class="snippet">
                  <code>docker tag myapp {{ info.image_prefix }}/myapp:1.0</code>
                  <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`docker tag myapp ${info.image_prefix}/myapp:1.0`)"><span class="mdi mdi-content-copy"></span></button>
                </div>
              </li>
              <li>
                <div class="step-label">3. Push</div>
                <div class="snippet">
                  <code>docker push {{ info.image_prefix }}/myapp:1.0</code>
                  <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`docker push ${info.image_prefix}/myapp:1.0`)"><span class="mdi mdi-content-copy"></span></button>
                </div>
              </li>
            </ol>
            <div class="app-banner app-banner--info" style="margin-top: 16px">
              <span class="mdi mdi-information-outline app-banner-icon"></span>
              <div class="app-banner-content">
                <p class="app-banner-text">
                  Pushed images deploy like any other image-source app — create an application from
                  <code class="mono">{{ info.image_prefix }}/myapp:1.0</code>.
                </p>
              </div>
            </div>
          </div>
        </div>
      </template>
    </template>

  </div>
</template>

<style scoped>
.page-header { display: flex; align-items: center; gap: 12px; }
.host-pill {
  padding: 4px 10px; border-radius: var(--radius); background: var(--bg-tertiary);
  color: var(--text-secondary); font-size: 13px;
}
.tab .mdi { font-size: 15px; margin-right: 4px; }

/* Empty / disabled states */
.empty { display: flex; align-items: center; gap: 16px; }
.empty-icon { font-size: 36px; color: var(--text-muted); flex-shrink: 0; }
.empty-title { font-weight: 600; color: var(--text-primary); margin: 0 0 2px; }

/* Repositories */
.repos-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.repos-actions { display: flex; align-items: center; gap: 8px; }
.search { position: relative; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; width: 260px; max-width: 100%; }
.repo-list { display: flex; flex-direction: column; }
/* The repository name is the link into the image; the row itself stays a plain
   row so the list reads as content rather than a stack of buttons. */
.repo-name.link {
  border: 0; background: none; padding: 0; font-family: inherit; font-size: 14px;
  color: var(--text-primary); cursor: pointer;
}
.repo-name.link:hover { color: var(--primary-500); text-decoration: underline; }
.more-note { align-self: center; }
.link-btn { background: none; border: 0; padding: 0; font: inherit; color: var(--primary-500); cursor: pointer; }
.link-btn:hover { text-decoration: underline; }
.repo { padding: 14px 16px; border-top: 1px solid var(--border-primary); }
.repo:first-child { border-top: 0; }
.repo-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.repo-icon { color: var(--primary-500); font-size: 18px; }
.repo-name { font-size: 14px; color: var(--text-primary); }
.tag-grid { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px; align-items: center; }
/* Chips are labels here, not controls — the per-tag actions live on the image
   page, so the padding is symmetric again. */
.tag {
  display: inline-flex; align-items: center; padding: 3px 10px; font-size: 12px;
  color: var(--text-primary);
  background: var(--bg-tertiary); border: 1px solid var(--border-primary); border-radius: var(--radius);
}

/* Connect */
.details-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; }
.detail { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.detail-label { font-size: 12px; color: var(--text-muted); }
.detail code { overflow: hidden; text-overflow: ellipsis; }
.steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 16px; }
.step-label { font-weight: 600; color: var(--text-primary); font-size: 14px; margin-bottom: 4px; }
.snippet {
  display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 6px;
  background: var(--bg-tertiary); border-radius: var(--radius); padding: 8px 12px;
  font-family: monospace; font-size: 13px; overflow-x: auto;
}
.snippet code { white-space: nowrap; }

.mono { font-family: monospace; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
.mdi-spin { animation: mdi-spin 0.9s linear infinite; }
@keyframes mdi-spin { to { transform: rotate(360deg); } }
</style>
