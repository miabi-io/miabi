<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useInboxStore } from '@/stores/inbox'
import type { InboxNotification } from '@/api/inbox'

const store = useInboxStore()
const { unread, items, loading } = storeToRefs(store)
const router = useRouter()

const open = ref(false)
const badge = computed(() => (unread.value > 99 ? '99+' : String(unread.value)))

function toggle() {
  open.value = !open.value
  if (open.value) store.loadRecent()
}
function close() {
  open.value = false
}

async function activate(n: InboxNotification) {
  if (!n.read_at) await store.markRead([n.id])
  close()
  if (n.subject_link) router.push(n.subject_link)
}

function sevClass(s: string) {
  return s === 'critical' ? 'sev-crit' : s === 'warning' ? 'sev-warn' : 'sev-info'
}
function sevIcon(s: string) {
  return s === 'critical'
    ? 'mdi-alert-octagon'
    : s === 'warning'
      ? 'mdi-alert'
      : 'mdi-information-outline'
}
function timeAgo(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

function onDocClick(e: MouseEvent) {
  if (open.value && !(e.target as HTMLElement).closest?.('.bell')) close()
}

onMounted(() => {
  store.connect()
  document.addEventListener('click', onDocClick)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  // The bell mounts once with the dashboard layout, so unmount == leaving the app
  // (logout): tear the SSE stream down.
  store.disconnect()
})
</script>

<template>
  <div class="bell">
    <button class="bell-btn" aria-label="Notifications" @click.stop="toggle">
      <span class="mdi mdi-bell-outline"></span>
      <span v-if="unread > 0" class="bell-badge">{{ badge }}</span>
    </button>

    <Transition name="dropdown">
      <div v-if="open" class="bell-dropdown" @click.stop>
        <div class="bell-head">
          <span>Notifications</span>
          <button v-if="unread > 0" class="bell-link" @click="store.markAllRead()">Mark all read</button>
        </div>

        <div class="bell-list">
          <div v-if="loading && !items.length" class="bell-empty"><span class="spinner"></span></div>
          <div v-else-if="!items.length" class="bell-empty">
            <span class="mdi mdi-bell-check-outline" style="font-size: 32px; color: var(--text-muted)"></span>
            <p>You're all caught up.</p>
          </div>
          <button
            v-for="n in items"
            :key="n.id"
            class="bell-item"
            :class="{ unread: !n.read_at }"
            @click="activate(n)"
          >
            <span class="mdi bell-sev" :class="[sevClass(n.severity), sevIcon(n.severity)]"></span>
            <span class="bell-body">
              <span class="bell-title">{{ n.title }}</span>
              <span class="bell-sub">{{ n.body }}</span>
              <span class="bell-time">{{ timeAgo(n.created_at) }}</span>
            </span>
            <span v-if="!n.read_at" class="bell-dot"></span>
          </button>
        </div>

        <div class="bell-foot">
          <RouterLink to="/notifications" class="bell-link" @click="close">View all →</RouterLink>
        </div>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.bell { position: relative; }
.bell-btn { position: relative; background: none; border: none; cursor: pointer; color: var(--text-secondary); padding: 8px; border-radius: 8px; font-size: 20px; line-height: 1; }
.bell-btn:hover { background: var(--bg-hover); color: var(--text-primary); }
.bell-badge { position: absolute; top: 2px; right: 2px; min-width: 16px; height: 16px; padding: 0 4px; border-radius: 8px; background: var(--danger-500); color: #fff; font-size: 10px; font-weight: 700; line-height: 16px; text-align: center; }

.bell-dropdown { position: absolute; top: calc(100% + 8px); right: 0; width: 360px; max-width: 90vw; background: var(--bg-secondary); border: 1px solid var(--border-primary); border-radius: 12px; box-shadow: var(--shadow-lg, 0 10px 30px rgba(0,0,0,0.25)); overflow: hidden; z-index: 50; }
.bell-head { display: flex; justify-content: space-between; align-items: center; padding: 12px 14px; border-bottom: 1px solid var(--border-primary); font-weight: 600; font-size: 14px; color: var(--text-primary); }
.bell-link { background: none; border: none; cursor: pointer; color: var(--primary-500); font-size: 12px; text-decoration: none; }
.bell-list { max-height: 380px; overflow-y: auto; }
.bell-empty { text-align: center; padding: 28px 16px; color: var(--text-muted); font-size: 13px; }
.bell-item { display: flex; gap: 10px; align-items: flex-start; width: 100%; text-align: left; background: none; border: none; border-bottom: 1px solid var(--border-primary); padding: 11px 14px; cursor: pointer; }
.bell-item:hover { background: var(--bg-hover); }
.bell-item.unread { background: color-mix(in srgb, var(--primary-500) 6%, transparent); }
.bell-sev { font-size: 18px; margin-top: 1px; flex-shrink: 0; }
.sev-crit { color: var(--danger-500); }
.sev-warn { color: var(--warning-600); }
.sev-info { color: var(--text-tertiary); }
.bell-body { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.bell-title { font-size: 13px; font-weight: 600; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bell-sub { font-size: 12px; color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bell-time { font-size: 11px; color: var(--text-muted); }
.bell-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--primary-500); flex-shrink: 0; margin-top: 5px; }
.bell-foot { padding: 10px 14px; text-align: center; border-top: 1px solid var(--border-primary); }
</style>
