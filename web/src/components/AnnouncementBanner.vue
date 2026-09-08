<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useInboxStore } from '@/stores/inbox'
import type { InboxNotification } from '@/api/inbox'
import { followNotificationLink } from '@/utils/notificationLink'
import AnnouncementBannerItem from './AnnouncementBannerItem.vue'

// Renders the pinned notices an administrator broadcast. They sit above the page
// rather than behind the bell because they change what the reader should do right
// now; dismissal is per-user and permanent. A notice marked undismissable has no
// close control and stays until it expires or is retracted.
const store = useInboxStore()
const { banners } = storeToRefs(store)
const router = useRouter()

function follow(n: InboxNotification) {
  followNotificationLink(router, n.subject_link)
}
</script>

<template>
  <div v-if="banners.length" class="banners">
    <AnnouncementBannerItem
      v-for="n in banners"
      :key="n.id"
      :severity="n.severity"
      :title="n.title"
      :body="n.body"
      :action-text="n.action_text"
      :has-action="!!n.subject_link"
      :dismissible="n.dismissal !== 'never'"
      @follow="follow(n)"
      @dismiss="store.dismiss([n.id])"
    />
  </div>
</template>

<style scoped>
.banners {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}
</style>
