<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '@/stores/workspace'
import AnnouncementBannerItem from './AnnouncementBannerItem.vue'

const { t, d } = useI18n()
const ws = useWorkspaceStore()

const restriction = computed(() => ws.currentWorkspace?.restriction || '')

const body = computed(() => {
  const w = ws.currentWorkspace
  if (!w) return ''
  const parts = [t(restriction.value === 'suspended' ? 'operator.restriction.suspendedBody' : 'operator.restriction.readOnlyBody')]
  if (w.restriction_reason) parts.push(t('operator.restriction.reason', { reason: w.restriction_reason }))
  if (w.restriction_until) parts.push(t('operator.restriction.until', { date: d(new Date(w.restriction_until), 'date') }))
  return parts.join(' ')
})
</script>

<template>
  <AnnouncementBannerItem
    v-if="restriction"
    :severity="restriction === 'suspended' ? 'critical' : 'warning'"
    :title="t(restriction === 'suspended' ? 'operator.restriction.suspendedTitle' : 'operator.restriction.readOnlyTitle')"
    :body="body"
  />
</template>
