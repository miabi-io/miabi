<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { migrationApi } from '@/api/workspaceMigration'
import AnnouncementBannerItem from './AnnouncementBannerItem.vue'
import ConfirmDialog from './ConfirmDialog.vue'

const { t, d } = useI18n()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const restriction = computed(() => ws.currentWorkspace?.restriction || '')

const copy = computed(() => {
  switch (restriction.value) {
    case 'suspended':
      return { title: 'operator.restriction.suspendedTitle', body: 'operator.restriction.suspendedBody' }
    case 'frozen':
      return { title: 'operator.restriction.frozenTitle', body: 'operator.restriction.frozenBody' }
    default:
      return { title: 'operator.restriction.readOnlyTitle', body: 'operator.restriction.readOnlyBody' }
  }
})

const body = computed(() => {
  const w = ws.currentWorkspace
  if (!w) return ''
  const parts = [t(copy.value.body)]
  if (w.restriction_reason) parts.push(t('operator.restriction.reason', { reason: w.restriction_reason }))
  if (w.restriction_until) parts.push(t('operator.restriction.until', { date: d(new Date(w.restriction_until), 'date') }))
  return parts.join(' ')
})

const canUnfreeze = computed(() => restriction.value === 'frozen' && ws.isWorkspaceOwner)
const confirmOpen = ref(false)
const busy = ref(false)

async function unfreeze() {
  const id = ws.currentWorkspace?.id
  if (!id) return
  busy.value = true
  try {
    await migrationApi.unfreeze(id)
    notify.success(t('wsMigration.unfrozen'))
    confirmOpen.value = false
    await ws.fetchWorkspaces()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <template v-if="restriction">
    <AnnouncementBannerItem
      :severity="restriction === 'suspended' ? 'critical' : 'warning'"
      :title="t(copy.title)"
      :body="body"
      :has-action="canUnfreeze"
      :action-text="t('wsMigration.unfreeze')"
      @follow="confirmOpen = true"
    />
    <ConfirmDialog
      :open="confirmOpen"
      :title="t('wsMigration.unfreezeTitle')"
      :message="t('wsMigration.unfreezeMessage')"
      :confirm-label="t('wsMigration.unfreeze')"
      :busy="busy"
      @confirm="unfreeze"
      @cancel="confirmOpen = false"
    />
  </template>
</template>
