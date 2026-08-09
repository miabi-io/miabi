<script setup lang="ts">
import { onMounted, ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { marketplaceApi } from '@/api/marketplace'
import type { CatalogEntry, TemplateInstallView, UninstallResult } from '@/api/marketplace'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()


type BrowseSource = 'all' | 'official' | 'community' | 'custom'
type Tab = BrowseSource | 'installed'
const BROWSE_SOURCES: BrowseSource[] = ['all', 'official', 'community', 'custom']
const PER_PAGE = 24

const tab = ref<Tab>('all')
const templates = ref<CatalogEntry[]>([])
const installs = ref<TemplateInstallView[]>([])
const loading = ref(false)

// Per-tab search text and page, so switching tabs preserves each one's state.
const searchBy = ref<Record<BrowseSource, string>>({ all: '', official: '', community: '', custom: '' })
const pageBy = ref<Record<BrowseSource, number>>({ all: 1, official: 1, community: 1, custom: 1 })

const sourceCounts = computed(() => {
  const c = { all: templates.value.length, official: 0, custom: 0, community: 0 }
  for (const t of templates.value) {
    if (t.source === 'official') c.official++
    else if (t.source === 'custom') c.custom++
    else if (t.source === 'community') c.community++
  }
  return c
})


const browseTabs = computed<{ key: BrowseSource; label: string }[]>(() => {
  const out: { key: BrowseSource; label: string }[] = [
    { key: 'all', label: 'All' },
    { key: 'official', label: 'Official' },
  ]
  if (sourceCounts.value.community) out.push({ key: 'community', label: 'Community' })
  out.push({ key: 'custom', label: 'Custom' })
  return out
})

const isBrowse = computed(() => tab.value !== 'installed')

function matchesSearch(t: CatalogEntry, q: string): boolean {
  if (!q) return true
  return (
    t.display_name.toLowerCase().includes(q) ||
    t.name.toLowerCase().includes(q) ||
    t.description.toLowerCase().includes(q) ||
    t.category.toLowerCase().includes(q) ||
    (t.tags ?? []).some((g: string) => g.toLowerCase().includes(q))
  )
}

// activeSearch binds the header search box to the current tab's state and
// resets that tab to page 1 on every keystroke.
const activeSearch = computed<string>({
  get: () => (isBrowse.value ? searchBy.value[tab.value as BrowseSource] : ''),
  set: (v) => {
    if (!isBrowse.value) return
    const src = tab.value as BrowseSource
    searchBy.value[src] = v
    pageBy.value[src] = 1
  },
})

// activeList is the current tab's templates after source + search filtering.
// The All tab skips the source filter and shows every synced and imported
// template together.
const activeList = computed(() => {
  if (!isBrowse.value) return []
  const src = tab.value as BrowseSource
  const q = searchBy.value[src].trim().toLowerCase()
  return templates.value.filter((t) => (src === 'all' || t.source === src) && matchesSearch(t, q))
})

const totalPages = computed(() => Math.max(1, Math.ceil(activeList.value.length / PER_PAGE)))
// clampedPage keeps the page valid when the list shrinks (e.g. after a search).
const clampedPage = computed(() =>
  isBrowse.value ? Math.min(pageBy.value[tab.value as BrowseSource], totalPages.value) : 1,
)
const pagedList = computed(() => {
  const start = (clampedPage.value - 1) * PER_PAGE
  return activeList.value.slice(start, start + PER_PAGE)
})

function setPage(p: number) {
  if (!isBrowse.value) return
  pageBy.value[tab.value as BrowseSource] = Math.min(Math.max(1, p), totalPages.value)
}


const ELLIPSIS = '…'
const pageWindow = computed<(number | typeof ELLIPSIS)[]>(() => {
  const total = totalPages.value
  const cur = clampedPage.value
  if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1)
  const out: (number | typeof ELLIPSIS)[] = [1]
  const start = Math.max(2, cur - 1)
  const end = Math.min(total - 1, cur + 1)
  if (start > 2) out.push(ELLIPSIS)
  for (let p = start; p <= end; p++) out.push(p)
  if (end < total - 1) out.push(ELLIPSIS)
  out.push(total)
  return out
})

// emptyMessage tailors the empty state per browse tab.
const emptyMessage = computed(() => {
  if (searchBy.value[tab.value as BrowseSource]?.trim()) return 'Try a different search term.'
  switch (tab.value) {
    case 'custom':
      return 'Import your own template manifest to add a custom template.'
    case 'community':
      return 'No community templates have synced from the registry.'
    default:
      return 'No marketplace templates are available.'
  }
})

async function loadTemplates() {
  if (!ws.currentWorkspaceId) return
  templates.value = (await marketplaceApi.templates(ws.currentWorkspaceId)).data.data ?? []
}
async function loadInstalls() {
  if (!ws.currentWorkspaceId) return
  installs.value = (await marketplaceApi.installs(ws.currentWorkspaceId)).data.data ?? []
}

onMounted(async () => {
  loading.value = true
  try {
    await Promise.all([loadTemplates(), loadInstalls()])
    // Deep link: ?tab=<all|official|community|custom|installed> opens that tab.
    const q = route.query.tab
    if (q === 'installed' || (typeof q === 'string' && BROWSE_SOURCES.includes(q as BrowseSource))) {
      tab.value = q as Tab
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})

// Keep the active tab in the URL so a reload / back-forward / shared link lands
// on the same tab. Uses replace (no history spam); omits the param for the
// default All tab to keep URLs clean.
watch(tab, (t) => {
  const query = { ...route.query }
  if (t === 'all') delete query.tab
  else query.tab = t
  if (query.tab !== route.query.tab) router.replace({ query })
})

// ---- presentation helpers ----
function isMdi(icon?: string) {
  return !!icon && icon.startsWith('mdi-')
}
function fallbackIcon(t: CatalogEntry) {
  return t.db_only ? 'mdi-database-outline' : 'mdi-cube-outline'
}
function provisionSummary(t: CatalogEntry): string {
  const parts: string[] = []
  if (t.applications) parts.push(`${t.applications} app${t.applications > 1 ? 's' : ''}`)
  if (t.databases) parts.push(`${t.databases} database${t.databases > 1 ? 's' : ''}`)
  if (t.volumes) parts.push(`${t.volumes} volume${t.volumes > 1 ? 's' : ''}`)
  return parts.join(' · ') || 'no dependencies'
}
function sourceBadge(source: string) {
  if (source === 'custom') return { label: 'Custom', cls: 'badge-warning' }
  if (source === 'community') return { label: 'Community', cls: 'badge-info' }
  return { label: 'Official', cls: 'badge-success' }
}

function openInstall(t: CatalogEntry) {
  router.push({ name: 'template-install', params: { slug: t.name } })
}

// ---- import ----
const importOpen = ref(false)
const importing = ref(false)
const importYaml = ref('')

async function doImport() {
  if (!ws.currentWorkspaceId || !importYaml.value.trim()) return
  importing.value = true
  try {
    const entry = (await marketplaceApi.import(ws.currentWorkspaceId, importYaml.value)).data.data
    notify.success(`Imported ${entry?.display_name ?? 'template'}`)
    importOpen.value = false
    importYaml.value = ''
    await loadTemplates()
    tab.value = 'custom' // surface the freshly imported template
    pageBy.value.custom = 1
  } catch (e) {
    notify.apiError(e)
  } finally {
    importing.value = false
  }
}

// ---- installed lifecycle ----
const busyInstall = ref<number | null>(null)

// Upgrades are driven from the template detail page (marketplace/<slug>?upgrade);
// the Installed-tab Upgrade button links there.

const uninstallTarget = ref<TemplateInstallView | null>(null)
// Typed-name confirmation guards an uninstall, as on app/database deletion.
const uninstallConfirm = ref('')

function openUninstall(i: TemplateInstallView) {
  uninstallConfirm.value = ''
  uninstallTarget.value = i
}

// deletionSummary lists what an uninstall will remove, from the install record.
function deletionSummary(i: TemplateInstallView): string {
  const parts: string[] = []
  const apps = i.app_ids?.length ?? 0
  const dbs = i.database_ids?.length ?? 0
  const vols = i.volume_ids?.length ?? 0
  if (apps) parts.push(`${apps} app${apps > 1 ? 's' : ''}`)
  if (dbs) parts.push(`${dbs} database${dbs > 1 ? 's' : ''}`)
  if (vols) parts.push(`${vols} volume${vols > 1 ? 's' : ''}`)
  return parts.join(', ') || 'its resources'
}

// Teardown follow-up: after an uninstall, show which resources were removed (and
// any that failed).
const teardown = ref<{ name: string; result: UninstallResult } | null>(null)
const teardownItems = computed(() => teardown.value?.result.removed ?? [])
const teardownFailed = computed(() => (teardown.value?.result.failed ?? 0) > 0)

async function confirmUninstall() {
  const i = uninstallTarget.value
  if (!i || !ws.currentWorkspaceId || uninstallConfirm.value !== i.template_display_name) return
  busyInstall.value = i.id
  try {
    const result = (await marketplaceApi.uninstall(ws.currentWorkspaceId, i.id)).data.data
    const name = i.template_display_name
    uninstallTarget.value = null
    await loadInstalls()
    if (result) {
      teardown.value = { name, result }
      if (result.failed > 0) notify.error(`${name}: some resources could not be removed`)
      else notify.success(`Uninstalled ${name}`)
    } else {
      notify.success(`Uninstalled ${name}`)
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    busyInstall.value = null
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Marketplace</h1>
        <p class="subtitle">One-click app templates you can deploy into {{ ws.contextLabel }}.</p>
      </div>
      <div class="header-actions">
        <div v-if="isBrowse" class="search-box">
          <span class="mdi mdi-magnify"></span>
          <input v-model="activeSearch" class="form-input" placeholder="Search templates…" aria-label="Search templates" />
        </div>
        <a
          class="btn btn-ghost contribute-link"
          href="https://github.com/miabi-io/marketplace/blob/main/CONTRIBUTING.md"
          target="_blank"
          rel="noopener noreferrer"
          title="Add an app to the open-source marketplace on GitHub"
        >
          <span class="mdi mdi-github"></span> <span class="contribute-label">Contribute</span>
        </a>
        <button v-if="ws.canEdit" class="btn btn-secondary" @click="importOpen = true">
          <span class="mdi mdi-upload"></span> Import
        </button>
      </div>
    </div>

    <div class="tabs">
      <button
        v-for="bt in browseTabs"
        :key="bt.key"
        class="tab"
        :class="{ active: tab === bt.key }"
        @click="tab = bt.key"
      >
        {{ bt.label }}
        <span class="tab-count">{{ sourceCounts[bt.key] }}</span>
      </button>
      <button class="tab" :class="{ active: tab === 'installed' }" @click="tab = 'installed'">
        Installed
        <span v-if="installs.length" class="tab-count">{{ installs.length }}</span>
      </button>
    </div>

    <div v-if="loading" class="loading-page"><span class="spinner"></span></div>

    <!-- BROWSE (All / Official / Community / Custom) -->
    <template v-else-if="isBrowse">
      <div v-if="activeList.length === 0" class="card">
        <div class="empty-state">
          <span class="mdi mdi-storefront-outline" style="font-size: 44px; color: var(--text-muted)"></span>
          <h3>No templates found</h3>
          <p>{{ emptyMessage }}</p>
          <button v-if="tab === 'custom' && ws.canEdit" class="btn btn-secondary" style="margin-top: 12px" @click="importOpen = true">
            <span class="mdi mdi-upload"></span> Import a template
          </button>
          <a
            v-if="tab === 'community'"
            class="btn btn-secondary"
            style="margin-top: 12px"
            href="https://github.com/miabi-io/marketplace/blob/main/CONTRIBUTING.md"
            target="_blank"
            rel="noopener noreferrer"
          >
            <span class="mdi mdi-github"></span> Contribute a template
          </a>
        </div>
      </div>

      <template v-else>
        <div class="template-grid">
          <div v-for="t in pagedList" :key="t.source + ':' + t.name" class="card template-card" @click="openInstall(t)">
            <div class="card-body">
              <div class="template-head">
                <span class="template-icon">
                  <img v-if="t.icon && !isMdi(t.icon)" :src="t.icon" :alt="t.display_name" />
                  <span v-else class="mdi" :class="isMdi(t.icon) ? t.icon : fallbackIcon(t)"></span>
                </span>
                <div class="template-tags">
                  <span class="badge" :class="sourceBadge(t.source).cls">{{ sourceBadge(t.source).label }}</span>
                  <span class="badge badge-neutral">{{ t.category }}</span>
                </div>
              </div>
              <h3 class="template-name">{{ t.display_name }}</h3>
              <p class="template-desc">{{ t.description }}</p>
              <div class="template-provision"><span class="mdi mdi-cube-scan"></span> {{ provisionSummary(t) }}</div>
              <div class="template-foot">
                <div class="template-meta">
                  <span class="cell-sub">v{{ t.version }}</span>
                  <span v-if="t.author" class="cell-sub template-author">by {{ t.author.name }}</span>
                </div>
                <button v-if="ws.canEdit" class="btn btn-sm btn-primary" @click.stop="openInstall(t)">Install</button>
              </div>
            </div>
          </div>
        </div>

        <template v-if="totalPages > 1">
          <div class="pagination">
            <button
              class="page-btn"
              :disabled="clampedPage <= 1"
              aria-label="Previous page"
              @click="setPage(clampedPage - 1)"
            >
              <span class="mdi mdi-chevron-left"></span>
            </button>
            <button
              v-for="(p, idx) in pageWindow"
              :key="idx"
              class="page-btn"
              :class="{ active: p === clampedPage, ellipsis: p === ELLIPSIS }"
              :disabled="p === ELLIPSIS"
              :aria-current="p === clampedPage ? 'page' : undefined"
              @click="typeof p === 'number' && setPage(p)"
            >
              {{ p }}
            </button>
            <button
              class="page-btn"
              :disabled="clampedPage >= totalPages"
              aria-label="Next page"
              @click="setPage(clampedPage + 1)"
            >
              <span class="mdi mdi-chevron-right"></span>
            </button>
          </div>
          <p class="page-summary">{{ activeList.length }} templates</p>
        </template>
      </template>
    </template>

    <!-- INSTALLED -->
    <template v-else>
      <div v-if="installs.length === 0" class="card">
        <div class="empty-state">
          <span class="mdi mdi-package-variant" style="font-size: 44px; color: var(--text-muted)"></span>
          <h3>Nothing installed yet</h3>
          <p>Install a template from the Browse tab to see it here.</p>
        </div>
      </div>
      <div v-else class="card">
        <table class="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Applications</th>
              <th>Version</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="i in installs" :key="i.id">
              <td>
                <div class="cell-main">{{ i.template_display_name }}</div>
                <div class="cell-sub">
                  <span class="badge" :class="sourceBadge(i.source).cls">{{ sourceBadge(i.source).label }}</span>
                  <span style="margin-left: 6px">{{ new Date(i.created_at).toLocaleDateString() }}</span>
                </div>
              </td>
              <td>
                <div v-if="i.apps && i.apps.length" class="install-apps">
                  <router-link
                    v-for="a in i.apps"
                    :key="a.id"
                    :to="{ name: 'app-detail', params: { id: a.id } }"
                    class="app-link"
                  >
                    <span class="status-dot" :class="`status-${a.status}`"></span>{{ a.display_name || a.name }}
                  </router-link>
                </div>
                <span v-else class="cell-sub">—</span>
              </td>
              <td>
                <span>v{{ i.version }}</span>
                <span v-if="i.update_available" class="badge badge-primary" style="margin-left: 6px">
                  v{{ i.latest_version }} available
                </span>
              </td>
              <td class="row-actions">
                <router-link
                  v-if="ws.canEdit && i.update_available"
                  class="btn btn-sm btn-secondary"
                  :to="{ name: 'template-install', params: { slug: i.template_name }, query: { upgrade: '1' } }"
                >
                  <span class="mdi mdi-arrow-up-bold-circle-outline"></span> Upgrade
                </router-link>
                <button
                  v-if="ws.isWorkspaceAdmin"
                  class="btn btn-sm btn-danger"
                  :disabled="busyInstall === i.id"
                  @click="openUninstall(i)"
                >
                  <span class="mdi mdi-delete-outline"></span> Uninstall
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- UNINSTALL CONFIRMATION -->
    <Teleport to="body">
      <div v-if="uninstallTarget" class="modal-overlay" @click.self="uninstallTarget = null">
        <div class="modal modal-sm">
          <div class="modal-header">
            <h3>Uninstall {{ uninstallTarget.template_display_name }}?</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="uninstallTarget = null"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="confirmUninstall">
            <div class="modal-body">
              <div class="danger-note">
                <span class="mdi mdi-alert-outline"></span>
                <div>
                  This permanently deletes <strong>{{ deletionSummary(uninstallTarget) }}</strong> created by this
                  install. This cannot be undone.
                </div>
              </div>
              <div class="form-group" style="margin-bottom: 0; margin-top: 12px">
                <label class="form-label">Type <code>{{ uninstallTarget.template_display_name }}</code> to confirm</label>
                <input v-model="uninstallConfirm" class="form-input" :placeholder="uninstallTarget.template_display_name" autofocus autocomplete="off" aria-label="Type template name to confirm" />
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="uninstallTarget = null" :disabled="busyInstall === uninstallTarget.id">
                Cancel
              </button>
              <button type="submit" class="btn btn-danger" :disabled="uninstallConfirm !== uninstallTarget.template_display_name || busyInstall === uninstallTarget.id">
                {{ busyInstall === uninstallTarget.id ? 'Uninstalling…' : 'Uninstall' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- UNINSTALL FOLLOW-UP: what the teardown removed -->
    <Teleport to="body">
      <div v-if="teardown" class="modal-overlay" @click.self="teardown = null">
        <div class="modal">
          <div class="modal-header">
            <h3>Resources removed — {{ teardown.name }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="teardown = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <p v-if="teardownFailed" class="teardown-summary failed">
              <span class="mdi mdi-alert-circle-outline"></span>
              {{ teardownItems.length - (teardown.result.failed) }} removed, {{ teardown.result.failed }} failed — the failed resources may need manual cleanup.
            </p>
            <p v-else class="teardown-summary ok">
              <span class="mdi mdi-check-circle-outline"></span>
              {{ teardownItems.length }} resource{{ teardownItems.length === 1 ? '' : 's' }} removed.
            </p>
            <p v-if="teardownItems.length === 0" class="text-muted text-sm">This install had no resources to remove.</p>
            <ul v-else class="teardown-list">
              <li v-for="(it, i) in teardownItems" :key="i">
                <span class="badge" :class="it.error ? 'badge-danger' : 'badge-neutral'">{{ it.error ? 'failed' : 'removed' }}</span>
                <span class="teardown-kind">{{ it.kind }}</span>
                <span class="mono teardown-name">{{ it.name }}</span>
                <span v-if="it.error" class="teardown-err">{{ it.error }}</span>
              </li>
            </ul>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="teardown = null">Close</button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- IMPORT -->
    <Teleport to="body">
      <div v-if="importOpen" class="modal-overlay" @click.self="importOpen = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Import a template</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="importOpen = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="doImport">
            <div class="modal-body">
              <p class="text-muted text-sm" style="margin-bottom: 12px">
                Paste a Miabi template manifest (<code>apiVersion: miabi.io/v1</code>). It is validated and
                added to this workspace as a custom template.
              </p>
              <textarea
                v-model="importYaml"
                class="form-input mono"
                rows="14"
                aria-label="Template manifest YAML"
                placeholder="apiVersion: miabi.io/v1&#10;kind: Template&#10;metadata:&#10;  name: my-app&#10;  displayName: My App&#10;  version: 1.0.0&#10;applications:&#10;  - name: app&#10;    image: nginx"
              ></textarea>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="importOpen = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="importing || !importYaml.trim()">
                {{ importing ? 'Importing…' : 'Import' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.search-box {
  position: relative;
  display: flex;
  align-items: center;
}
.search-box .mdi {
  position: absolute;
  left: 10px;
  color: var(--text-muted);
  font-size: 18px;
  pointer-events: none;
}
.search-box .form-input {
  padding-left: 34px;
  width: 240px;
}
.tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border-primary);
  margin-bottom: 20px;
}
.tab {
  background: none;
  border: none;
  padding: 10px 16px;
  font-size: 14px;
  font-weight: 500;
  color: var(--text-muted);
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}
.tab.active {
  color: var(--primary-600);
  border-bottom-color: var(--primary-600);
}
.tab-count {
  display: inline-block;
  min-width: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--primary-50);
  color: var(--text-muted);
  font-size: 11px;
  line-height: 18px;
  text-align: center;
  margin-left: 4px;
}
.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 20px;
}
.page-btn {
  min-width: 34px;
  height: 34px;
  padding: 0 10px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1;
  cursor: pointer;
  transition:
    background var(--transition),
    color var(--transition),
    border-color var(--transition);
}
.page-btn:hover:not(:disabled):not(.active) {
  background: var(--primary-50);
  color: var(--primary-600);
  border-color: var(--primary-200, var(--border-primary));
}
.page-btn.active {
  background: var(--primary-600);
  border-color: var(--primary-600);
  color: #fff;
  font-weight: 600;
  cursor: default;
}
.page-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.page-btn.ellipsis {
  border: none;
  background: none;
  opacity: 1;
  min-width: 20px;
  padding: 0;
  color: var(--text-muted);
}
.page-summary {
  text-align: center;
  font-size: 12px;
  color: var(--text-muted);
  margin: 8px 0 0;
}
.template-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 16px;
}
.template-card {
  cursor: pointer;
  transition: transform var(--transition), box-shadow var(--transition);
}
.template-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}
.template-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 12px;
}
.template-tags {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
}
.template-icon {
  width: 40px;
  height: 40px;
  border-radius: var(--radius);
  background: var(--primary-50);
  color: var(--primary-600);
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  flex-shrink: 0;
}
.template-icon img {
  width: 26px;
  height: 26px;
  object-fit: contain;
}
.template-icon .mdi {
  font-size: 22px;
}
.template-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}
.template-desc {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 4px;
  min-height: 2.6em;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.template-provision {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 10px;
  display: flex;
  align-items: center;
  gap: 5px;
}
.template-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 16px;
}
.template-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.template-author {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.text-muted {
  color: var(--text-muted);
}
.install-apps {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.app-link {
  font-size: 13px;
  color: var(--primary-600);
  display: inline-flex;
  align-items: center;
  gap: 6px;
  width: fit-content;
}
.app-link:hover {
  text-decoration: underline;
}
.status-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--text-muted);
  flex-shrink: 0;
}
.status-running {
  background: var(--success-500, #16a34a);
}
.status-failed {
  background: var(--danger-500, #dc2626);
}
.status-deploying {
  background: var(--warning-500, #d97706);
}
.modal-sm {
  max-width: 440px;
}
.danger-note {
  display: flex;
  gap: 10px;
  font-size: 13px;
  color: var(--text-primary);
  background: var(--danger-50, rgba(220, 38, 38, 0.06));
  border: 1px solid var(--danger-200, rgba(220, 38, 38, 0.2));
  border-radius: var(--radius);
  padding: 12px;
}
.danger-note .mdi {
  color: var(--danger-500);
  font-size: 20px;
  flex-shrink: 0;
}
.contribute-link { color: var(--text-secondary); }
.contribute-link .mdi { font-size: 17px; }
@media (max-width: 640px) {
  .search-box .form-input {
    width: 150px;
  }
  /* Collapse the contribute link to its icon to save header space. */
  .contribute-label { display: none; }
}
.teardown-summary { display: flex; align-items: center; gap: 8px; font-size: 14px; margin: 0 0 14px; }
.teardown-summary.ok { color: var(--success-600); }
.teardown-summary.failed { color: var(--danger-500); }
.teardown-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.teardown-list li { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.teardown-kind { color: var(--text-secondary); font-size: 13px; text-transform: capitalize; }
.teardown-name { font-size: 13px; }
.teardown-err { color: var(--danger-500); font-size: 12px; flex-basis: 100%; padding-left: 4px; }
</style>
