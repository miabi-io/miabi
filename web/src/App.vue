<script setup lang="ts">
import { useThemeStore } from '@/stores/theme'
import { useNotificationStore } from '@/stores/notification'
import AdminUnlockDialog from '@/components/AdminUnlockDialog.vue'

useThemeStore() // apply theme on boot
const notify = useNotificationStore()
</script>

<template>
  <div>
    <router-view />
    <AdminUnlockDialog />
    <div class="toast-container" role="region" aria-label="Notifications" aria-live="polite">
      <div
        v-for="n in notify.notifications"
        :key="n.id"
        class="toast"
        :class="`toast-${n.type}`"
        :role="n.type === 'error' ? 'alert' : 'status'"
        tabindex="0"
        @click="notify.dismiss(n.id)"
        @keydown.enter="notify.dismiss(n.id)"
        @keydown.space.prevent="notify.dismiss(n.id)"
      >
        <div v-if="n.title" class="toast-title">{{ n.title }}</div>
        <div class="toast-message">{{ n.message }}</div>
        <div v-if="n.detail" class="toast-detail">{{ n.detail }}</div>
      </div>
    </div>
  </div>
</template>
