<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useInboxStore } from '@/stores/inbox'
import type { InboxNotification } from '@/api/inbox'
import { followNotificationLink } from '@/utils/notificationLink'

// Renders the pinned notices an administrator broadcast. They sit above the page
// rather than behind the bell because they change what the reader should do right
// now; dismissal is per-user and permanent. A notice marked undismissable has no
// close control and stays until it expires or is retracted.
const store = useInboxStore()
const { banners } = storeToRefs(store)
const router = useRouter()

function sevClass(s: string) {
  return s === 'critical' ? 'ban-crit' : s === 'warning' ? 'ban-warn' : 'ban-info'
}
function sevIcon(s: string) {
  return s === 'critical' ? 'mdi-alert-octagon' : s === 'warning' ? 'mdi-alert' : 'mdi-bullhorn-outline'
}
function follow(n: InboxNotification) {
  followNotificationLink(router, n.subject_link)
}
</script>

<template>
  <div v-if="banners.length" class="banners">
    <div v-for="n in banners" :key="n.id" class="banner" :class="sevClass(n.severity)">
      <span class="mdi ban-icon" :class="sevIcon(n.severity)"></span>
      <div class="ban-body">
        <div class="ban-title">{{ n.title }}</div>
        <div v-if="n.body" class="ban-text">{{ n.body }}</div>
      </div>
      <button v-if="n.subject_link" class="btn btn-secondary btn-sm" @click="follow(n)">
        {{ n.action_text || 'Open' }}
      </button>
      <button
        v-if="n.dismissal !== 'never'"
        class="btn-icon btn-icon-muted"
        aria-label="Dismiss"
        @click="store.dismiss([n.id])"
      >
        <span class="mdi mdi-close"></span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.banners {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}

.banner {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid var(--border-primary);
  border-left-width: 3px;
  border-radius: 8px;
  background: var(--bg-secondary);
}

.ban-icon {
  font-size: 20px;
  flex-shrink: 0;
}

.ban-info {
  border-left-color: var(--primary-500);
}
.ban-info .ban-icon {
  color: var(--primary-500);
}

.ban-warn {
  border-left-color: var(--warning-600);
}
.ban-warn .ban-icon {
  color: var(--warning-600);
}

.ban-crit {
  border-left-color: var(--danger-500);
}
.ban-crit .ban-icon {
  color: var(--danger-500);
}

.ban-body {
  flex: 1;
  min-width: 0;
}

.ban-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}

.ban-text {
  font-size: 12px;
  color: var(--text-secondary);
  margin-top: 2px;
}
</style>
