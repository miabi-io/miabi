<script setup lang="ts">
import { computed, nextTick, ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { searchApi } from '@/api/search'
import { navSections, type NavItem } from '@/data/nav'
import type { SearchKind, SearchResult } from '@/api/types'

const props = defineProps<{ open: boolean; docsEnabled?: boolean; docsUrl?: string }>()
const emit = defineEmits<{ (e: 'update:open', value: boolean): void }>()

const router = useRouter()
const auth = useAuthStore()
const ws = useWorkspaceStore()

type Entry = {
  id: string
  group: string
  label: string
  sub?: string
  icon: string
  to?: string
  href?: string
  kind?: SearchKind
}

const RECENTS_KEY = 'mb_palette_recents'
const MIN_QUERY = 2

const query = ref('')
const loading = ref(false)
const failed = ref(false)
const results = ref<SearchResult[]>([])
const active = ref(0)
const inputEl = ref<HTMLInputElement | null>(null)
const listEl = ref<HTMLElement | null>(null)

const kindMeta: Record<SearchKind, { label: string; icon: string; route: (r: SearchResult) => string }> = {
  application: { label: 'Applications', icon: 'mdi-cube-outline', route: (r) => `/apps/${r.id}` },
  stack: { label: 'Stacks', icon: 'mdi-layers-outline', route: (r) => `/stacks/${r.id}` },
  database: { label: 'Databases', icon: 'mdi-database-outline', route: (r) => `/databases/${r.id}` },
  volume: { label: 'Volumes', icon: 'mdi-harddisk', route: (r) => `/volumes/${r.id}` },
  network: { label: 'Networks', icon: 'mdi-lan', route: () => '/networks' },
  domain: { label: 'Domains', icon: 'mdi-web', route: () => '/domains' },
  route: { label: 'Routes', icon: 'mdi-routes', route: (r) => `/routes/${r.id}` },
  certificate: { label: 'Certificates', icon: 'mdi-certificate', route: (r) => `/certificates/${r.id}` },
  secret: { label: 'Secrets', icon: 'mdi-key-variant', route: () => '/secrets' },
  config: { label: 'Configs', icon: 'mdi-file-cog-outline', route: () => '/configs' },
  pipeline: { label: 'Pipelines', icon: 'mdi-pipe', route: (r) => `/pipelines/${r.id}/runs` },
  gitsource: { label: 'GitOps', icon: 'mdi-source-branch-sync', route: (r) => `/gitops/${r.id}` },
  environment: { label: 'Environments', icon: 'mdi-layers-triple-outline', route: () => '/environments' },
  registry: { label: 'Registries', icon: 'mdi-database-lock-outline', route: () => '/registries' },
  gitrepository: { label: 'Git Repositories', icon: 'mdi-git', route: () => '/git-repositories' },
}

const navEntries = computed<Entry[]>(() => {
  const out: Entry[] = []
  for (const section of navSections) {
    for (const item of section.items) {
      if (item.requiresAdmin && !auth.isAdmin) continue
      if (item.requiresWorkspace && !ws.isWorkspaceContext) continue
      if (item.requiresWorkspaceAdmin && !(ws.isWorkspaceContext && ws.isWorkspaceAdmin)) continue
      if (item.requiresDocs && !props.docsEnabled) continue
      out.push({
        id: `nav:${section.id}:${item.name}`,
        group: section.title,
        label: item.name,
        sub: section.title,
        icon: item.icon,
        to: item.external ? undefined : navPath(item),
        href: item.external ? props.docsUrl : undefined,
      })
    }
  }
  return out
})

function navPath(item: NavItem): string {
  if (item.workspaceTab) return `/workspaces/${ws.currentWorkspaceId}?tab=${item.workspaceTab}`
  return item.path
}

const workspaceEntries = computed<Entry[]>(() =>
  ws.workspaces.map((w) => ({
    id: `ws:${w.id}`,
    group: 'Switch workspace',
    label: w.display_name || w.name,
    sub: w.id === ws.currentWorkspaceId ? 'Current' : undefined,
    icon: 'mdi-briefcase-outline',
    to: `__workspace:${w.id}`,
  })),
)

const trimmed = computed(() => query.value.trim())
const bareTerm = computed(() => {
  const idx = trimmed.value.indexOf(':')
  return idx > 0 ? trimmed.value.slice(idx + 1).trim() : trimmed.value
})

function matches(text: string, term: string): boolean {
  return text.toLowerCase().includes(term.toLowerCase())
}

const matchedNav = computed<Entry[]>(() => {
  const term = trimmed.value
  if (!term) return navEntries.value
  if (term.startsWith('@')) return []
  return navEntries.value
    .filter((e) => matches(e.label, bareTerm.value) || matches(e.group, bareTerm.value))
    .sort((a, b) => rank(b.label, bareTerm.value) - rank(a.label, bareTerm.value))
    .slice(0, 8)
})

const matchedWorkspaces = computed<Entry[]>(() => {
  const term = trimmed.value
  if (!term.startsWith('@')) return []
  const q = term.slice(1).trim()
  if (!q) return workspaceEntries.value.slice(0, 8)
  return workspaceEntries.value.filter((e) => matches(e.label, q)).slice(0, 8)
})

function rank(label: string, term: string): number {
  const l = label.toLowerCase()
  const t = term.toLowerCase()
  if (l === t) return 3
  if (l.startsWith(t)) return 2
  return 1
}

const resourceEntries = computed<Entry[]>(() =>
  results.value.map((r) => {
    const meta = kindMeta[r.kind]
    return {
      id: `res:${r.kind}:${r.id}`,
      group: meta?.label ?? r.kind,
      label: r.display_name || r.name,
      sub: r.detail || (r.display_name ? r.name : undefined),
      icon: meta?.icon ?? 'mdi-shape-outline',
      to: meta?.route(r) ?? '/',
      kind: r.kind,
    }
  }),
)

const recents = ref<Entry[]>(loadRecents())

const recentEntries = computed<Entry[]>(() =>
  trimmed.value ? [] : recents.value.slice(0, 5).map((e) => ({ ...e, group: 'Recent' })),
)

const entries = computed<Entry[]>(() => [
  ...matchedWorkspaces.value,
  ...recentEntries.value,
  ...resourceEntries.value,
  ...matchedNav.value,
])

const groups = computed(() => {
  const out: { title: string; items: Entry[] }[] = []
  for (const e of entries.value) {
    const last = out[out.length - 1]
    if (last && last.title === e.group) last.items.push(e)
    else out.push({ title: e.group, items: [e] })
  }
  return out
})

const flatIndex = computed(() => entries.value.map((e) => e.id))

let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | null = null

watch(query, () => {
  active.value = 0
  if (timer) clearTimeout(timer)
  controller?.abort()
  failed.value = false

  const term = bareTerm.value
  if (!ws.isWorkspaceContext || term.length < MIN_QUERY || trimmed.value.startsWith('@')) {
    results.value = []
    loading.value = false
    return
  }
  loading.value = true
  timer = setTimeout(runSearch, 180)
})

async function runSearch() {
  const wsId = ws.currentWorkspaceId
  if (!wsId) return
  controller = new AbortController()
  const sent = trimmed.value
  try {
    const { data } = await searchApi.search(wsId, sent, 20, controller.signal)
    if (sent !== trimmed.value) return
    results.value = data.data.results ?? []
  } catch (err) {
    if ((err as { code?: string })?.code === 'ERR_CANCELED') return
    results.value = []
    failed.value = true
  } finally {
    if (sent === trimmed.value) loading.value = false
  }
}

function close() {
  emit('update:open', false)
}

function move(delta: number) {
  const n = flatIndex.value.length
  if (!n) return
  active.value = (active.value + delta + n) % n
  nextTick(scrollActiveIntoView)
}

function scrollActiveIntoView() {
  const el = listEl.value?.querySelector<HTMLElement>('[data-active="true"]')
  el?.scrollIntoView({ block: 'nearest' })
}

function isActive(entry: Entry): boolean {
  return flatIndex.value[active.value] === entry.id
}

function select(entry: Entry, newTab = false) {
  if (entry.href) {
    window.open(entry.href, '_blank', 'noopener')
    close()
    return
  }
  if (!entry.to) return

  if (entry.to.startsWith('__workspace:')) {
    ws.setWorkspace(Number(entry.to.split(':')[1]))
    close()
    router.push('/')
    return
  }

  remember(entry)
  if (newTab) {
    window.open(router.resolve(entry.to).href, '_blank', 'noopener')
    close()
    return
  }
  close()
  router.push(entry.to)
}

function onEnter(event: KeyboardEvent) {
  const entry = entries.value[active.value]
  if (entry) select(entry, event.metaKey || event.ctrlKey)
}

function loadRecents(): Entry[] {
  try {
    const raw = localStorage.getItem(RECENTS_KEY)
    return raw ? (JSON.parse(raw) as Entry[]) : []
  } catch {
    return []
  }
}

function remember(entry: Entry) {
  if (!entry.kind) return
  const next = [entry, ...recents.value.filter((e) => e.id !== entry.id)].slice(0, 8)
  recents.value = next
  try {
    localStorage.setItem(RECENTS_KEY, JSON.stringify(next))
  } catch {
    /* storage unavailable */
  }
}

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      if (timer) clearTimeout(timer)
      controller?.abort()
      return
    }
    query.value = ''
    results.value = []
    active.value = 0
    recents.value = loadRecents()
    await nextTick()
    inputEl.value?.focus()
  },
)

function onKeydown(event: KeyboardEvent) {
  if (!props.open) return
  if (event.key === 'Escape') {
    event.preventDefault()
    close()
  }
}

onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  if (timer) clearTimeout(timer)
  controller?.abort()
})

const hint = computed(() => {
  if (!ws.isWorkspaceContext) return 'Select a workspace to search its resources'
  if (failed.value) return 'Search is unavailable right now'
  if (loading.value) return 'Searching…'
  if (trimmed.value && bareTerm.value.length < MIN_QUERY) return `Type at least ${MIN_QUERY} characters`
  return ''
})
</script>

<template>
  <Teleport to="body">
    <Transition name="palette">
      <div v-if="open" class="palette-backdrop" @click.self="close">
        <div class="palette" role="dialog" aria-modal="true" aria-label="Search">
          <div class="palette-input-row">
            <span class="mdi mdi-magnify palette-input-icon"></span>
            <input
              ref="inputEl"
              v-model="query"
              class="palette-input"
              type="text"
              placeholder="Search resources, jump to a page, @ to switch workspace"
              autocomplete="off"
              spellcheck="false"
              @keydown.down.prevent="move(1)"
              @keydown.up.prevent="move(-1)"
              @keydown.enter.prevent="onEnter"
            />
            <span v-if="loading" class="spinner palette-spinner"></span>
            <button class="palette-esc" type="button" @click="close">esc</button>
          </div>

          <div ref="listEl" class="palette-list">
            <div v-if="hint" class="palette-hint">{{ hint }}</div>

            <template v-for="group in groups" :key="group.title">
              <div class="palette-group">{{ group.title }}</div>
              <button
                v-for="entry in group.items"
                :key="entry.id"
                type="button"
                class="palette-item"
                :class="{ active: isActive(entry) }"
                :data-active="isActive(entry)"
                @click="select(entry, $event.metaKey || $event.ctrlKey)"
                @mousemove="active = flatIndex.indexOf(entry.id)"
              >
                <span class="mdi palette-item-icon" :class="entry.icon"></span>
                <span class="palette-item-text">
                  <span class="palette-item-label">{{ entry.label }}</span>
                  <span v-if="entry.sub" class="palette-item-sub">{{ entry.sub }}</span>
                </span>
                <span v-if="entry.href" class="mdi mdi-open-in-new palette-item-ext"></span>
              </button>
            </template>

            <div v-if="!groups.length && !hint" class="palette-empty">
              <span class="mdi mdi-magnify-close"></span>
              <span>No matches for “{{ trimmed }}”</span>
            </div>
          </div>

          <div class="palette-footer">
            <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
            <span><kbd>↵</kbd> open</span>
            <span><kbd>⌘</kbd><kbd>↵</kbd> new tab</span>
            <span class="palette-footer-spacer"></span>
            <span><kbd>app:</kbd><kbd>db:</kbd><kbd>route:</kbd> filter</span>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.palette-backdrop {
  position: fixed;
  inset: 0;
  z-index: 3000;
  background: rgba(15, 15, 20, 0.45);
  backdrop-filter: blur(2px);
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding: 12vh 16px 16px;
}
.palette {
  width: 100%;
  max-width: 640px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: 12px;
  box-shadow: 0 24px 64px rgba(0, 0, 0, 0.28);
  display: flex;
  flex-direction: column;
  max-height: 70vh;
  overflow: hidden;
}
.palette-input-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-primary);
}
.palette-input-icon {
  font-size: 20px;
  color: var(--text-muted);
}
.palette-input {
  flex: 1;
  border: 0;
  outline: none;
  background: transparent;
  font-size: 15px;
  color: var(--text-primary);
  min-width: 0;
}
.palette-input::placeholder {
  color: var(--text-muted);
}
.palette-spinner {
  width: 14px;
  height: 14px;
}
.palette-esc {
  border: 1px solid var(--border-primary);
  background: var(--bg-secondary);
  color: var(--text-muted);
  border-radius: 6px;
  padding: 2px 8px;
  font-size: 11px;
  cursor: pointer;
}
.palette-list {
  overflow-y: auto;
  padding: 6px;
  flex: 1;
}
.palette-group {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
  padding: 10px 10px 4px;
}
.palette-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  color: var(--text-primary);
}
.palette-item.active {
  background: var(--bg-hover, var(--bg-secondary));
}
.palette-item-icon {
  font-size: 18px;
  color: var(--text-muted);
  flex-shrink: 0;
}
.palette-item-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  flex: 1;
}
.palette-item-label {
  font-size: 14px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.palette-item-sub {
  font-size: 12px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.palette-item-ext {
  font-size: 14px;
  color: var(--text-muted);
}
.palette-hint,
.palette-empty {
  padding: 18px 12px;
  color: var(--text-muted);
  font-size: 13px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}
.palette-footer {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 8px 14px;
  border-top: 1px solid var(--border-primary);
  font-size: 11px;
  color: var(--text-muted);
  flex-wrap: wrap;
}
.palette-footer-spacer {
  flex: 1;
}
.palette-footer kbd {
  border: 1px solid var(--border-primary);
  border-radius: 4px;
  padding: 1px 5px;
  margin-right: 3px;
  font-family: inherit;
  background: var(--bg-secondary);
}
.palette-enter-active,
.palette-leave-active {
  transition: opacity 0.12s ease;
}
.palette-enter-from,
.palette-leave-to {
  opacity: 0;
}
@media (max-width: 640px) {
  .palette-backdrop {
    padding: 8vh 10px 10px;
  }
  .palette-footer {
    display: none;
  }
}
</style>
