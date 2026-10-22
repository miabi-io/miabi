<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import AnnouncementBannerItem from './AnnouncementBannerItem.vue'

const { t, d } = useI18n()
const auth = useAuthStore()

// The server enforces the end date; the banner only hides once it has passed.
const suspended = computed(() => {
  const u = auth.user
  if (!u?.suspended_at) return false
  return !u.suspended_until || new Date(u.suspended_until).getTime() > Date.now()
})

const body = computed(() => {
  const u = auth.user
  const parts = [t('operator.suspension.bannerBody')]
  if (u?.suspended_reason) parts.push(t('operator.restriction.reason', { reason: u.suspended_reason }))
  if (u?.suspended_until) parts.push(t('operator.restriction.until', { date: d(new Date(u.suspended_until), 'date') }))
  return parts.join(' ')
})
</script>

<template>
  <AnnouncementBannerItem v-if="suspended" severity="critical" :title="t('operator.suspension.bannerTitle')" :body="body" />
</template>
