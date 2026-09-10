<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import AnnouncementBanner from '@/components/AnnouncementBanner.vue'
import ConsoleShell from './ConsoleShell.vue'
import { navSections } from '@/data/nav'
import { workspaceApi } from '@/api/workspaces'
import type { PendingInvitation } from '@/api/types'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const license = useLicenseStore()

// Pending invitations addressed to the current user. The Dashboard renders these
// too, but a user with no workspaces never reaches it — the empty state below
// replaces <router-view>. Without this, an invitee's only way out is to create a
// workspace they don't need.
const invitations = ref<PendingInvitation[]>([])
const acceptingId = ref<number | null>(null)

async function loadInvitations() {
  try {
    invitations.value = (await workspaceApi.myInvitations()).data.data ?? []
  } catch {
    // Non-critical: the empty state still offers "Create workspace".
  }
}

async function acceptInvitation(inv: PendingInvitation) {
  acceptingId.value = inv.id
  try {
    await workspaceApi.acceptInvitation(inv.id)
    notify.success(`Joined ${inv.workspace_name}`)
    await ws.fetchWorkspaces()
    ws.setWorkspace(inv.workspace_id)
    await loadInvitations()
  } catch (e) {
    notify.apiError(e, 'Failed to accept invitation')
  } finally {
    acceptingId.value = null
  }
}

function createWorkspace() {
  router.push('/workspaces?create=1')
}

onMounted(async () => {
  // A cached profile from before preferences existed has no `preferences` key, so
  // refresh it — the workspace store and the theme both read fields that only a
  // current /me carries.
  if (!auth.user || !auth.user.preferences) await auth.fetchUser()
  if (auth.isAdmin) license.load().catch(() => { })
  try {
    await ws.fetchWorkspaces()
  } catch {
    notify.error('Failed to load workspaces')
  }
  // Only when the empty state is what the user will actually see; with a
  // workspace present the Dashboard fetches these itself.
  if (ws.workspaces.length === 0) await loadInvitations()
})
</script>

<template>
  <ConsoleShell :sections="navSections" console="workspace" section-state-key="mb_nav_sections" home="/">
    <template #banners>
      <AnnouncementBanner />
    </template>

    <div v-if="ws.loaded && ws.workspaces.length === 0 && route.path !== '/workspaces' && !route.meta.noWorkspace"
      class="empty-state">
      <!-- An invitee has somewhere to go that isn't "create a workspace". -->
      <template v-if="invitations.length">
        <span class="mdi mdi-email-outline" style="font-size: 48px; color: var(--text-muted)"></span>
        <h3>You've been invited</h3>
        <p>Accept an invitation to join a workspace.</p>
        <ul class="empty-invites">
          <li v-for="inv in invitations" :key="inv.id" class="empty-invite">
            <div class="empty-invite-info">
              <span class="empty-invite-name">{{ inv.workspace_name }}</span>
              <span class="empty-invite-sub">
                Invited as <strong>{{ inv.role }}</strong>
                <template v-if="inv.invited_by_name"> by {{ inv.invited_by_name }}</template>
              </span>
            </div>
            <button class="btn btn-primary btn-sm" :disabled="acceptingId === inv.id" @click="acceptInvitation(inv)">
              {{ acceptingId === inv.id ? 'Joining…' : 'Accept' }}
            </button>
          </li>
        </ul>
        <button class="btn btn-secondary mt-4" @click="createWorkspace">
          Or create your own workspace
        </button>
      </template>
      <template v-else>
        <span class="mdi mdi-briefcase-plus-outline" style="font-size: 48px; color: var(--text-muted)"></span>
        <h3>No workspaces yet</h3>
        <p>Create your first workspace to deploy applications.</p>
        <button class="btn btn-primary mt-4" @click="createWorkspace">Create workspace</button>
      </template>
    </div>
    <router-view v-else />
  </ConsoleShell>
</template>

<style scoped>
.empty-invites {
  list-style: none;
  margin: 20px 0 0;
  padding: 0;
  width: 100%;
  max-width: 480px;
  border: 1px solid var(--border-secondary);
  border-radius: 8px;
  text-align: left;
}

.empty-invite {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
}

.empty-invite+.empty-invite {
  border-top: 1px solid var(--border-secondary);
}

.empty-invite-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.empty-invite-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}

.empty-invite-sub {
  font-size: 13px;
  color: var(--text-muted);
}

.empty-invite-sub strong {
  color: var(--text-secondary);
  text-transform: capitalize;
}
</style>
