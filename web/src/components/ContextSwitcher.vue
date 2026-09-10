<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { ADMIN_HOME, rememberConsoleRoute, workspaceReturnPath } from '@/data/console'

const props = defineProps<{
  // 'admin' renders the platform identity and offers the way back; 'workspace'
  // lists the workspaces and offers the way in.
  mode: 'workspace' | 'admin'
  collapsed?: boolean
}>()
const emit = defineEmits<{ navigate: [] }>()

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const license = useLicenseStore()

const open = ref(false)
const isAdminConsole = computed(() => props.mode === 'admin')

const label = computed(() => (isAdminConsole.value ? 'Platform' : ws.contextLabel))
const initial = computed(() => {
  if (isAdminConsole.value) return 'P'
  const name = ws.currentWorkspace?.display_name || ws.currentWorkspace?.name
  return name?.charAt(0)?.toUpperCase() || 'D'
})

// A licence problem is the one platform condition worth surfacing from inside a
// workspace: the banner explaining it now lives in the admin console, so without
// this an admin would have to go looking.
const adminAttention = computed(() => auth.isAdmin && license.warnings.length > 0)

function close() {
  open.value = false
  emit('navigate')
}

function switchWorkspace(id: number) {
  ws.setWorkspace(id)
  close()
  // Leaving the admin console by picking a workspace lands on that workspace's
  // dashboard, not on whatever admin page was open.
  if (isAdminConsole.value || route.path !== '/') router.push('/')
}

// makeDefaultWorkspace pins where future sessions land. Separate from switching on
// purpose: navigating between workspaces should not silently rewrite the default.
async function makeDefaultWorkspace(id: number) {
  try {
    await ws.makeDefault(id)
    notify.success('Default workspace saved')
  } catch (e) {
    notify.apiError(e)
  }
}

function go(path: string) {
  close()
  router.push(path)
}

function enterAdmin() {
  rememberConsoleRoute('workspace', route.fullPath)
  go(ADMIN_HOME)
}

function leaveAdmin() {
  rememberConsoleRoute('admin', route.fullPath)
  go(workspaceReturnPath())
}
</script>

<template>
  <div class="ws-switcher" :class="{ 'ws-switcher-admin': isAdminConsole }">
    <div class="ws-switcher-toggle" @click.stop="open = !open">
      <div class="ws-switcher-current">
        <div class="ws-avatar">
          <span v-if="isAdminConsole" class="mdi mdi-shield-crown-outline"></span>
          <template v-else>{{ initial }}</template>
        </div>
        <span v-if="!collapsed" class="ws-switcher-name">
          {{ label }}
          <span v-if="isAdminConsole" class="ws-admin-badge">Admin</span>
        </span>
      </div>
      <span v-if="!collapsed" class="mdi mdi-unfold-more-horizontal ws-switcher-chevron"></span>
      <span v-if="!isAdminConsole && adminAttention && collapsed" class="ws-attention-dot"></span>
    </div>

    <Transition name="dropdown">
      <div v-if="open" class="ws-switcher-dropdown">
        <div v-for="w in ws.workspaces" :key="w.id" class="ws-switcher-option"
          :class="{ active: !isAdminConsole && ws.currentWorkspaceId === w.id }" @click="switchWorkspace(w.id)">
          <div class="ws-avatar-sm">{{ (w.display_name || w.name).charAt(0).toUpperCase() }}</div>
          <span class="ws-switcher-option-name">
            {{ w.display_name || w.name }}
            <span class="ws-switcher-option-handle">{{ w.name }}</span>
          </span>
          <span v-if="w.role" class="ws-role-badge">{{ w.role }}</span>
          <span v-if="auth.user?.default_workspace_id === w.id" class="mdi mdi-pin ws-default-pin"
            title="Sessions land here by default"></span>
          <button v-else class="mdi mdi-pin-outline ws-default-set" title="Make this my default workspace"
            aria-label="Make this my default workspace" @click.stop="makeDefaultWorkspace(w.id)"></button>
        </div>
        <div v-if="!ws.workspaces.length" class="ws-switcher-empty">No workspaces yet</div>

        <div class="ws-switcher-divider"></div>
        <div class="ws-switcher-action" @click="go('/workspaces?create=1')">
          <span class="mdi mdi-plus"></span><span>Create workspace</span>
        </div>
        <div class="ws-switcher-action" @click="go('/workspaces')">
          <span class="mdi mdi-briefcase-outline"></span><span>Manage workspaces…</span>
        </div>

        <!-- The platform is not a workspace, so it sits below its own rule rather
             than as one more row in the list above. -->
        <template v-if="auth.isAdmin">
          <div class="ws-switcher-rule"></div>
          <div v-if="isAdminConsole" class="ws-switcher-action ws-switcher-leave" @click="leaveAdmin">
            <span class="mdi mdi-arrow-left"></span>
            <span>Back to {{ ws.isWorkspaceContext ? ws.contextLabel : 'workspaces' }}</span>
          </div>
          <div v-else class="ws-switcher-action ws-switcher-enter" @click="enterAdmin">
            <span class="mdi mdi-shield-crown-outline"></span>
            <span>Platform admin</span>
            <span v-if="adminAttention" class="ws-attention-dot ws-attention-inline"></span>
            <span class="mdi mdi-chevron-right ws-switcher-enter-chevron"></span>
          </div>
        </template>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
/* ─── Workspace switcher ─── */
.ws-switcher {
  padding: 0 8px 8px;
  position: relative;
}

.ws-switcher-toggle {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-radius: var(--radius);
  cursor: pointer;
  color: var(--sidebar-text);
  border: 1px solid var(--sidebar-border);
  transition: background var(--transition), color var(--transition);
}

.ws-switcher-toggle:hover {
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
}

.ws-switcher-current {
  display: flex;
  align-items: center;
  gap: 8px;
  overflow: hidden;
}

.ws-avatar {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  background: var(--primary-600);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}

.ws-avatar-sm {
  width: 22px;
  height: 22px;
  border-radius: 5px;
  background: var(--primary-600);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-weight: 600;
  flex-shrink: 0;
}

.ws-switcher-name {
  font-size: 13px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ws-switcher-chevron {
  font-size: 16px;
  opacity: 0.7;
  flex-shrink: 0;
}

.ws-switcher-dropdown {
  position: absolute;
  top: calc(100% + 2px);
  left: 8px;
  right: 8px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  padding: 4px;
  z-index: 200;
  min-width: 210px;
}

.ws-switcher-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: all var(--transition);
}

.ws-switcher-option:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ws-switcher-option.active {
  background: var(--primary-50);
  color: var(--primary-700);
}

.ws-switcher-option-name {
  display: flex;
  flex-direction: column;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ws-switcher-option-handle {
  font-size: 11px;
  font-family: monospace;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
}

.ws-role-badge {
  margin-left: auto;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--text-muted);
  letter-spacing: 0.05em;
  flex-shrink: 0;
}

/* The pin sits after the role badge, which already claims margin-left:auto. */
.ws-default-pin,
.ws-default-set {
  flex-shrink: 0;
  font-size: 14px;
  line-height: 1;
  background: none;
  border: none;
  padding: 2px;
  cursor: pointer;
}

.ws-default-pin { color: var(--primary-500); cursor: default; }
/* Hidden until the row is hovered or the button is focused, so the list stays calm
   but the action is still reachable from the keyboard. */
.ws-default-set { color: var(--text-muted); opacity: 0; transition: opacity 0.12s; }
.ws-switcher-option:hover .ws-default-set,
.ws-default-set:focus-visible { opacity: 1; }
.ws-default-set:hover { color: var(--primary-500); }

.ws-switcher-empty {
  padding: 10px;
  font-size: 13px;
  color: var(--text-muted);
  text-align: center;
}

.ws-switcher-divider {
  height: 1px;
  background: var(--border-primary);
  margin: 4px 6px;
}

.ws-switcher-action {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: all var(--transition);
}

.ws-switcher-action:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ws-switcher-action .mdi {
  font-size: 16px;
}

/* The collapsed sidebar hides the label, so the toggle centres its avatar. The
   parent layout owns .sidebar-collapsed, hence :global. */
:global(.sidebar-collapsed) .sidebar:not(.sidebar-mobile) .ws-switcher-toggle {
  justify-content: center;
  padding: 8px;
  border-color: transparent;
}

/* ─── Platform identity ─── */
.ws-switcher-admin .ws-avatar {
  background: var(--warning-600, #b45309);
}

.ws-admin-badge {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 5px;
  border-radius: 4px;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  vertical-align: middle;
  background: rgba(255, 255, 255, 0.16);
  color: #fff;
}

.ws-switcher-rule {
  height: 1px;
  margin: 6px 0;
  background: var(--border-secondary);
}

.ws-switcher-enter-chevron {
  margin-left: auto;
  font-size: 16px;
  opacity: 0.6;
}

.ws-switcher-enter .mdi:first-child,
.ws-switcher-leave .mdi:first-child {
  color: var(--warning-700, #a16207);
}

.ws-attention-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--warning-600, #d97706);
  flex-shrink: 0;
}

.ws-attention-inline {
  margin-left: 6px;
}

/* Transition classes land on the dropdown, which carries this component's scope. */
.dropdown-enter-active,
.dropdown-leave-active {
  transition: opacity 150ms ease, transform 150ms ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
