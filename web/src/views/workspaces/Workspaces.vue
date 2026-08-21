<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { copyText } from '@/utils/clipboard'
import AppModal from '@/components/AppModal.vue'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const showCreate = ref(false)
const saving = ref(false)
const form = ref({ name: '', description: '' })

function openCreate() {
  form.value = { name: '', description: '' }
  showCreate.value = true
}

async function submitCreate() {
  if (!form.value.name.trim()) return
  saving.value = true
  // The first user workspace (the system workspace doesn't count) is onboarding:
  // land on the Dashboard so the getting-started checklist guides the next step.
  // Later workspaces open their settings.
  const isFirst = !ws.workspaces.some((w) => !w.system)
  try {
    const created = await ws.create({ display_name: form.value.name.trim(), description: form.value.description.trim() })
    notify.success('Workspace created')
    showCreate.value = false
    router.push(isFirst ? '/' : `/workspaces/${created.id}?tab=settings`)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

function open(id: number) {
  ws.setWorkspace(id)
  router.push(`/workspaces/${id}?tab=settings`)
}

// Copy the workspace id — handy for API calls and binding an API key. Stops
// propagation so the card's navigate-on-click doesn't fire.
async function copyId(id: number) {
  if (await copyText(String(id))) notify.success('Workspace ID copied')
  else notify.error('Could not copy ID')
}

function roleBadgeClass(role?: string) {
  switch (role) {
    case 'owner':
      return 'badge-info'
    case 'admin':
      return 'badge-warning'
    case 'developer':
      return 'badge-success'
    default:
      return 'badge-neutral'
  }
}

onMounted(() => {
  if (!ws.loaded) ws.fetchWorkspaces().catch(() => {})
  if (route.query.create) openCreate()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Workspaces</h1>
      <button class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New workspace
      </button>
    </div>

    <div v-if="ws.workspaces.length === 0" class="card">
      <div class="empty-state">
        <span class="mdi mdi-briefcase-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No workspaces yet</h3>
        <p>Workspaces group your applications, databases, and team members.</p>
        <button class="btn btn-primary mt-4" @click="openCreate">Create your first workspace</button>
      </div>
    </div>

    <div v-else class="ws-grid">
      <button
        v-for="w in ws.workspaces"
        :key="w.id"
        class="ws-card"
        :class="{ active: ws.currentWorkspaceId === w.id }"
        @click="open(w.id)"
      >
        <div class="ws-card-top">
          <div class="ws-card-avatar">{{ (w.display_name || w.name).charAt(0).toUpperCase() }}</div>
          <span v-if="w.role" class="badge" :class="roleBadgeClass(w.role)">{{ w.role }}</span>
        </div>
        <div class="ws-card-name">
          {{ w.display_name || w.name }}
          <span v-if="w.privileged" class="badge badge-info" title="Privileged — host port bindings are auto-approved"><span class="mdi mdi-shield-check-outline"></span> privileged</span>
        </div>
        <div class="ws-card-handle mono">{{ w.name }}</div>
        <div class="ws-card-desc">{{ w.description || 'No description' }}</div>
        <div
          class="ws-card-id"
          role="button"
          tabindex="0"
          title="Copy workspace ID"
          @click.stop="copyId(w.id)"
          @keydown.enter.stop.prevent="copyId(w.id)"
        >
          <span class="ws-card-id-label">ID</span>
          <code>{{ w.id }}</code>
          <span class="mdi mdi-content-copy"></span>
        </div>
        <div v-if="ws.currentWorkspaceId === w.id" class="ws-card-current">
          <span class="mdi mdi-check-circle"></span> Current
        </div>
      </button>
    </div>

    <!-- Create modal -->
    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>Create workspace</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="submitCreate">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. Production" aria-label="Name" required autofocus />
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Description <span class="text-muted">(optional)</span></label>
              <input v-model="form.description" class="form-input" placeholder="What is this workspace for?" aria-label="Description" />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.name.trim()">
              {{ saving ? 'Creating…' : 'Create workspace' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.ws-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 16px;
}
.ws-card {
  text-align: left;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-lg);
  padding: 18px 20px;
  cursor: pointer;
  font-family: inherit;
  box-shadow: var(--shadow-sm);
  transition: transform var(--transition), box-shadow var(--transition), border-color var(--transition);
}
.ws-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
  border-color: var(--primary-300);
}
.ws-card.active {
  border-color: var(--primary-500);
}
.ws-card-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.ws-card-avatar {
  width: 40px;
  height: 40px;
  border-radius: var(--radius);
  background: var(--primary-600);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
  font-weight: 700;
}
.ws-card-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ws-card-handle {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mono {
  font-family: monospace;
}
.ws-card-desc {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ws-card-id {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-top: 12px;
  padding: 2px 8px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  font-size: 12px;
  color: var(--text-muted);
  cursor: pointer;
  transition: border-color var(--transition), color var(--transition);
}
.ws-card-id:hover {
  border-color: var(--primary-300);
  color: var(--text-primary);
}
.ws-card-id-label {
  font-size: 10px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.ws-card-id code {
  font-family: 'JetBrains Mono', monospace;
  color: var(--text-primary);
}
.ws-card-id .mdi {
  font-size: 13px;
}
.ws-card-current {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-top: 12px;
  font-size: 12px;
  font-weight: 600;
  color: var(--primary-600);
}
.ws-card-current .mdi {
  font-size: 15px;
}
.text-muted {
  color: var(--text-muted);
}
</style>
