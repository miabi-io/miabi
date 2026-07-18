<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { inboxApi, type InboxNotification } from '@/api/inbox'
import { useInboxStore } from '@/stores/inbox'
import { useWorkspaceStore } from '@/stores/workspace'

const router = useRouter()
const inbox = useInboxStore()
const ws = useWorkspaceStore()
const { workspaces } = storeToRefs(ws)

const items = ref<InboxNotification[]>([])
const loading = ref(false)
const unreadOnly = ref(false)
const wsFilter = ref<number>(0)
const done = ref(false)

async function load(reset = true) {
  loading.value = true
  try {
    const before = reset ? undefined : items.value[items.value.length - 1]?.id
    const res = await inboxApi.list({
      workspace: wsFilter.value || undefined,
      unread: unreadOnly.value || undefined,
      before,
      limit: 30,
    })
    const batch = res.data.data ?? []
    items.value = reset ? batch : [...items.value, ...batch]
    done.value = batch.length < 30
  } finally {
    loading.value = false
  }
}

async function open(n: InboxNotification) {
  if (!n.read_at) {
    await inbox.markRead([n.id])
    const it = items.value.find((x) => x.id === n.id)
    if (it) it.read_at = new Date().toISOString()
  }
  if (n.subject_link) router.push(n.subject_link)
}

async function markAll() {
  await inbox.markAllRead(wsFilter.value || undefined)
  await load()
}

function wsName(id: number) {
  const w = workspaces.value.find((x) => x.id === id)
  return w?.display_name || w?.name || `Workspace #${id}`
}
function sevClass(s: string) {
  return s === 'critical' ? 'sev-crit' : s === 'warning' ? 'sev-warn' : 'sev-info'
}
function sevIcon(s: string) {
  return s === 'critical' ? 'mdi-alert-octagon' : s === 'warning' ? 'mdi-alert' : 'mdi-information-outline'
}
function fmtTime(iso: string) {
  return new Date(iso).toLocaleString()
}

const hasUnread = computed(() => items.value.some((n) => !n.read_at))

watch([unreadOnly, wsFilter], () => load())
onMounted(() => load())
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Notifications</h1>
        <p class="subtitle">Alerts and updates across your workspaces.</p>
      </div>
      <button v-if="hasUnread" class="btn btn-secondary" @click="markAll">Mark all read</button>
    </div>

    <div class="filters">
      <select v-model.number="wsFilter" class="select">
        <option :value="0">All workspaces</option>
        <option v-for="w in workspaces" :key="w.id" :value="w.id">{{ w.display_name || w.name }}</option>
      </select>
      <label class="chk"><input v-model="unreadOnly" type="checkbox" /> Unread only</label>
    </div>

    <div class="card">
      <div v-if="loading && !items.length" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="!items.length" class="empty-state">
        <span class="mdi mdi-bell-check-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>Nothing here</h3>
        <p>{{ unreadOnly ? 'No unread notifications.' : 'You have no notifications yet.' }}</p>
      </div>
      <div v-else class="n-list">
        <button
          v-for="n in items"
          :key="n.id"
          class="n-item"
          :class="{ unread: !n.read_at }"
          @click="open(n)"
        >
          <span class="mdi n-sev" :class="[sevClass(n.severity), sevIcon(n.severity)]"></span>
          <span class="n-body">
            <span class="n-title">{{ n.title }}</span>
            <span class="n-text">{{ n.body }}</span>
            <span class="n-meta">
              <span class="badge badge-neutral">{{ wsName(n.workspace_id) }}</span>
              <span class="badge badge-neutral">{{ n.category }}</span>
              <span class="n-time">{{ fmtTime(n.created_at) }}</span>
            </span>
          </span>
          <span v-if="!n.read_at" class="n-dot"></span>
        </button>
      </div>
    </div>

    <div v-if="items.length && !done" class="load-more">
      <button class="btn btn-ghost" :disabled="loading" @click="load(false)">Load more</button>
    </div>
  </div>
</template>

<style scoped>
.filters { display: flex; gap: 14px; align-items: center; margin-bottom: 14px; flex-wrap: wrap; }
.select { padding: 7px 10px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); color: var(--text-primary); font-size: 13px; }
.chk { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-secondary); cursor: pointer; }

.n-list { display: flex; flex-direction: column; }
.n-item { display: flex; gap: 12px; align-items: flex-start; width: 100%; text-align: left; background: none; border: none; border-bottom: 1px solid var(--border-primary); padding: 14px 18px; cursor: pointer; }
.n-item:last-child { border-bottom: none; }
.n-item:hover { background: var(--bg-hover); }
.n-item.unread { background: color-mix(in srgb, var(--primary-500) 6%, transparent); }
.n-sev { font-size: 20px; margin-top: 1px; flex-shrink: 0; }
.sev-crit { color: var(--danger-500); }
.sev-warn { color: var(--warning-600); }
.sev-info { color: var(--text-tertiary); }
.n-body { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1; }
.n-title { font-size: 14px; font-weight: 600; color: var(--text-primary); }
.n-text { font-size: 13px; color: var(--text-secondary); }
.n-meta { display: flex; gap: 8px; align-items: center; margin-top: 2px; flex-wrap: wrap; }
.n-time { font-size: 12px; color: var(--text-muted); }
.n-dot { width: 9px; height: 9px; border-radius: 50%; background: var(--primary-500); flex-shrink: 0; margin-top: 5px; }
.load-more { text-align: center; margin-top: 14px; }
</style>
