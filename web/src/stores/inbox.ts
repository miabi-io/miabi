import { defineStore } from 'pinia'
import { ref } from 'vue'
import { inboxApi, type InboxNotification } from '@/api/inbox'

// Per-user notification inbox: unread badge + recent items, kept live over SSE.
// The stream is a nudge to refetch (Postgres is the source of truth), so the wire
// stays tiny and reconnection is trivial.
export const useInboxStore = defineStore('inbox', () => {
  const unread = ref(0)
  const items = ref<InboxNotification[]>([])
  // banners are the pinned, undismissed notices rendered above the page rather
  // than behind the bell — currently platform announcements.
  const banners = ref<InboxNotification[]>([])
  const loading = ref(false)
  let es: EventSource | null = null
  let refetchTimer: ReturnType<typeof setTimeout> | null = null

  async function loadUnread() {
    try {
      unread.value = (await inboxApi.unreadCount()).data.data?.unread ?? 0
    } catch {
      /* transient */
    }
  }

  async function loadBanners() {
    try {
      banners.value = (await inboxApi.banners()).data.data ?? []
    } catch {
      /* transient */
    }
  }

  async function loadRecent() {
    loading.value = true
    try {
      items.value = (await inboxApi.list({ limit: 20 })).data.data ?? []
    } catch {
      /* transient */
    } finally {
      loading.value = false
    }
  }

  // Coalesce bursts of SSE nudges (a crash-loop resolve touches many rows) into a
  // single refetch.
  function scheduleRefetch() {
    if (refetchTimer) return
    refetchTimer = setTimeout(() => {
      refetchTimer = null
      void loadUnread()
      void loadRecent()
      void loadBanners()
    }, 400)
  }

  function connect() {
    if (es) return
    void loadUnread()
    void loadBanners()
    es = new EventSource(inboxApi.streamUrl(), { withCredentials: true })
    es.onmessage = () => scheduleRefetch()
    es.onerror = () => {
      // EventSource auto-reconnects; nothing to do.
    }
  }

  function disconnect() {
    es?.close()
    es = null
    if (refetchTimer) {
      clearTimeout(refetchTimer)
      refetchTimer = null
    }
    banners.value = []
  }

  async function markRead(ids: number[]) {
    if (!ids.length) return
    await inboxApi.markRead(ids)
    const now = new Date().toISOString()
    for (const n of items.value) if (ids.includes(n.id) && !n.read_at) n.read_at = now
    await loadUnread()
  }

  // dismiss drops a notice from the banner for this user only. It is removed
  // locally first: the row stays in the inbox, so a refetch would bring it back
  // into the list but never into the banner.
  async function dismiss(ids: number[]) {
    if (!ids.length) return
    banners.value = banners.value.filter((n) => !ids.includes(n.id))
    await inboxApi.dismiss(ids)
    await loadUnread()
  }

  async function markAllRead(workspace?: number) {
    await inboxApi.markAllRead(workspace)
    const now = new Date().toISOString()
    for (const n of items.value) if (!n.read_at) n.read_at = now
    unread.value = 0
  }

  return {
    unread,
    items,
    banners,
    loading,
    connect,
    disconnect,
    loadUnread,
    loadRecent,
    loadBanners,
    markRead,
    markAllRead,
    dismiss,
  }
})
