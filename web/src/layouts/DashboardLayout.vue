<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import NotificationBell from '@/components/NotificationBell.vue'
import { useLicenseStore } from '@/stores/license'
import { infoApi } from '@/api/info'
import { workspaceApi } from '@/api/workspaces'
import { adminApi } from '@/api/admin'
import type { PendingInvitation, UpdateInfo } from '@/api/types'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const theme = useThemeStore()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const license = useLicenseStore()
const userMenuRef = ref<HTMLElement | null>(null)
const wsSwitcherRef = ref<HTMLElement | null>(null)

const sidebarCollapsed = ref(localStorage.getItem('mb_sidebar_collapsed') === 'true')
const mobileOpen = ref(false)
const userMenuOpen = ref(false)
const wsSwitcherOpen = ref(false)
const themeModes = ['light', 'dark', 'system'] as const

const user = computed(() => auth.user)
const currentYear = new Date().getFullYear()

// The interactive API reference (/docs) is served at the server root, a sibling
// of the API base prefix. Hidden until /info confirms it's enabled.
const docsEnabled = ref(false)
const docsUrl = ((import.meta.env.VITE_API_URL as string) || '/api/v1').replace(/\/api\/v1\/?$/, '') + '/docs'

interface NavItem {
  name: string
  path: string
  icon: string
  /** Only show when a workspace is selected. */
  requiresWorkspace?: boolean
  /** Only show to owners/admins of the current workspace. */
  requiresWorkspaceAdmin?: boolean
  /** Only show to the platform admin. */
  requiresAdmin?: boolean
  /** Only show when the /docs API reference is enabled. */
  requiresDocs?: boolean
  /** Render as an external link (opens docsUrl in a new tab) instead of a route. */
  external?: boolean
  /** Deep-link into a tab of the current workspace's detail page. */
  workspaceTab?: 'settings' | 'members' | 'audit' | 'notifications'
}

interface NavSection {
  id: string
  title: string
  items: NavItem[]
  defaultOpen?: boolean
}

const navSections: NavSection[] = [
  {
    id: 'overview',
    title: 'Overview',
    items: [{ name: 'Dashboard', path: '/', icon: 'mdi-view-dashboard-outline' }],
  },
  {
    id: 'analytics',
    title: 'Analytics',
    items: [
      { name: 'Overview', path: '/analytics', icon: 'mdi-chart-areaspline', requiresWorkspace: true },
      { name: 'HTTP Traffic', path: '/analytics/http', icon: 'mdi-earth', requiresWorkspace: true },
      { name: 'Performance', path: '/analytics/performance', icon: 'mdi-speedometer', requiresWorkspace: true },
      { name: 'Web Analytics', path: '/analytics/web', icon: 'mdi-account-group-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'deploy',
    title: 'Deploy',
    items: [
      { name: 'Applications', path: '/apps', icon: 'mdi-cube-outline', requiresWorkspace: true },
      { name: 'Stacks', path: '/stacks', icon: 'mdi-layers-outline', requiresWorkspace: true },
      { name: 'Jobs', path: '/jobs', icon: 'mdi-console-line', requiresWorkspace: true },
      { name: 'Marketplace', path: '/marketplace', icon: 'mdi-storefront-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'data',
    title: 'Data',
    items: [
      { name: 'Databases', path: '/databases', icon: 'mdi-database-outline', requiresWorkspace: true },
      { name: 'Volumes', path: '/volumes', icon: 'mdi-harddisk', requiresWorkspace: true },
    ],
  },
  {
    id: 'networking',
    title: 'Networking',
    items: [
      { name: 'Networks', path: '/networks', icon: 'mdi-lan', requiresWorkspace: true },
      { name: 'Domains', path: '/domains', icon: 'mdi-web', requiresWorkspace: true },
      { name: 'DNS Providers', path: '/dns-providers', icon: 'mdi-dns', requiresWorkspace: true },
      { name: 'Routes', path: '/routes', icon: 'mdi-routes', requiresWorkspace: true },
      { name: 'Middlewares', path: '/middlewares', icon: 'mdi-tune-vertical', requiresWorkspace: true },
      { name: 'Certificates', path: '/certificates', icon: 'mdi-certificate', requiresWorkspace: true },
    ],
  },
  {
    id: 'sources',
    title: 'Sources',
    items: [
      { name: 'Secrets', path: '/secrets', icon: 'mdi-key-variant', requiresWorkspace: true },
      { name: 'Registries', path: '/registries', icon: 'mdi-database-lock-outline', requiresWorkspace: true },
      { name: 'Git Repositories', path: '/git-repositories', icon: 'mdi-git', requiresWorkspace: true },
    ],
  },
  {
    id: 'cicd',
    title: 'GitOps & CI/CD',
    items: [
      { name: 'Pipelines', path: '/pipelines', icon: 'mdi-pipe', requiresWorkspace: true },
      { name: 'Runners', path: '/runners', icon: 'mdi-cog-transfer-outline', requiresWorkspace: true },
      { name: 'GitOps', path: '/gitops', icon: 'mdi-source-branch-sync', requiresWorkspace: true },
      { name: 'Releases', path: '/releases', icon: 'mdi-tag-outline', requiresWorkspace: true },
      { name: 'Environments', path: '/environments', icon: 'mdi-layers-triple-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'developers',
    title: 'Developers',
    items: [
      { name: 'API Keys', path: '/api-keys', icon: 'mdi-key-outline', requiresWorkspace: true },
      { name: 'Container Registry', path: '/registry', icon: 'mdi-cube-outline', requiresWorkspace: true },
      { name: 'Webhooks', path: '/webhooks', icon: 'mdi-webhook', requiresWorkspace: true },
      { name: 'API Reference', path: '', icon: 'mdi-book-open-page-variant-outline', external: true, requiresDocs: true },
    ],
  },
  {
    id: 'workspace',
    title: 'Workspace',
    items: [
      { name: 'All Workspaces', path: '/workspaces', icon: 'mdi-briefcase-outline' },
      { name: 'Members', path: '', icon: 'mdi-account-group-outline', workspaceTab: 'members', requiresWorkspaceAdmin: true },
      { name: 'Events', path: '/events', icon: 'mdi-timeline-text-outline', requiresWorkspace: true },
      { name: 'Audit Log', path: '/audit-log', icon: 'mdi-history', requiresWorkspaceAdmin: true },
      { name: 'Notifications', path: '', icon: 'mdi-bell-outline', workspaceTab: 'notifications', requiresWorkspaceAdmin: true },
      { name: 'Settings', path: '', icon: 'mdi-cog-outline', workspaceTab: 'settings', requiresWorkspaceAdmin: true },
    ],
  },
  {
    id: 'admin',
    title: 'Platform Admin',
    defaultOpen: true,
    items: [
      { name: 'Dashboard', path: '/admin/metrics', icon: 'mdi-view-dashboard-outline', requiresAdmin: true },
      { name: 'Users', path: '/admin/users', icon: 'mdi-account-group-outline', requiresAdmin: true },
      { name: 'Workspaces', path: '/admin/workspaces', icon: 'mdi-briefcase-outline', requiresAdmin: true },
      { name: 'Domains', path: '/admin/domains', icon: 'mdi-web', requiresAdmin: true },
      { name: 'Routes', path: '/admin/routes', icon: 'mdi-sitemap-outline', requiresAdmin: true },
      { name: 'Nodes', path: '/admin/nodes', icon: 'mdi-server-network', requiresAdmin: true },
      { name: 'Shared Runners', path: '/admin/runners', icon: 'mdi-cog-transfer-outline', requiresAdmin: true },
      { name: 'Events', path: '/admin/events', icon: 'mdi-pulse', requiresAdmin: true },
      { name: 'Jobs', path: '/admin/jobs', icon: 'mdi-clock-outline', requiresAdmin: true },
      { name: 'OAuth Providers', path: '/admin/oauth', icon: 'mdi-shield-key-outline', requiresAdmin: true },
      { name: 'LDAP / AD', path: '/admin/ldap', icon: 'mdi-account-key-outline', requiresAdmin: true },
      { name: 'Plans', path: '/admin/plans', icon: 'mdi-tune-variant', requiresAdmin: true },
      { name: 'License', path: '/admin/license', icon: 'mdi-license', requiresAdmin: true },
      { name: 'SIEM Streaming', path: '/admin/siem', icon: 'mdi-export-variant', requiresAdmin: true },
      { name: 'Platform Backup', path: '/admin/platform-backup', icon: 'mdi-cloud-upload-outline', requiresAdmin: true },
      { name: 'Container Registry', path: '/admin/registry', icon: 'mdi-cube-outline', requiresAdmin: true },
      { name: 'Platform Settings', path: '/admin/settings', icon: 'mdi-cog-outline', requiresAdmin: true },
      { name: 'Deployment Config', path: '/admin/deployment-config', icon: 'mdi-package-variant-closed', requiresAdmin: true },
    ],
  },
]

const SECTION_STATE_KEY = 'mb_nav_sections'
function loadSectionState(): Record<string, boolean> {
  const defaults: Record<string, boolean> = {}
  navSections.forEach((s) => (defaults[s.id] = s.defaultOpen !== false))
  try {
    return { ...defaults, ...JSON.parse(localStorage.getItem(SECTION_STATE_KEY) || '{}') }
  } catch {
    return defaults
  }
}
const sectionOpen = ref<Record<string, boolean>>(loadSectionState())
function toggleSection(id: string) {
  sectionOpen.value[id] = !sectionOpen.value[id]
  localStorage.setItem(SECTION_STATE_KEY, JSON.stringify(sectionOpen.value))
}

function sectionItems(section: NavSection): NavItem[] {
  return section.items.filter((item) => {
    if (item.requiresAdmin && !auth.isAdmin) return false
    if (item.requiresWorkspace && !ws.isWorkspaceContext) return false
    if (item.requiresWorkspaceAdmin && !(ws.isWorkspaceContext && ws.isWorkspaceAdmin)) return false
    if (item.requiresDocs && !docsEnabled.value) return false
    return true
  })
}
const visibleSections = computed(() =>
  navSections.filter((s) => sectionItems(s).length > 0),
)

function itemTo(item: NavItem): string {
  if (item.workspaceTab) return `/workspaces/${ws.currentWorkspaceId}?tab=${item.workspaceTab}`
  return item.path
}

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/'
  return route.path === path || route.path.startsWith(path + '/')
}
function isItemActive(item: NavItem): boolean {
  if (item.workspaceTab) {
    return (
      route.path === `/workspaces/${ws.currentWorkspaceId}` &&
      (route.query.tab || 'settings') === item.workspaceTab
    )
  }
  if (item.path === '/workspaces') return route.path === '/workspaces'
  return isActive(item.path)
}

function navigate(path: string) {
  router.push(path)
  mobileOpen.value = false
}

const toggleSidebar = () => {
  sidebarCollapsed.value = !sidebarCollapsed.value
  localStorage.setItem('mb_sidebar_collapsed', String(sidebarCollapsed.value))
}

function switchWorkspace(id: number) {
  ws.setWorkspace(id)
  wsSwitcherOpen.value = false
  mobileOpen.value = false
  if (route.path !== '/') router.push('/')
}
function goWorkspaces() {
  wsSwitcherOpen.value = false
  mobileOpen.value = false
  router.push('/workspaces')
}
function createWorkspace() {
  wsSwitcherOpen.value = false
  mobileOpen.value = false
  router.push('/workspaces?create=1')
}

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

function logout() {
  auth.logout()
  ws.clear()
  router.push('/login')
}

function closeMenus(e: MouseEvent) {
  const target = e.target as Node

  if (userMenuOpen.value && userMenuRef.value && !userMenuRef.value.contains(target)) {
    userMenuOpen.value = false
  }

  if (wsSwitcherOpen.value && wsSwitcherRef.value && !wsSwitcherRef.value.contains(target)) {
    wsSwitcherOpen.value = false
  }
}

// License banner: admins see a warning when the license is in grace, expired,
// nearing expiry, or over the node limit. Driven by the cached entitlements.
const licenseBanner = computed(() => {
  if (!auth.isAdmin) return null
  const w = license.warnings
  if (w.includes('license_expired')) {
    return { level: 'danger', text: 'Your license has expired. Enterprise features are now read-only.' }
  }
  if (w.includes('license_grace')) {
    return { level: 'warning', text: 'Your license has expired and is in its grace period — renew to avoid losing access to paid features.' }
  }
  if (w.includes('nearing_expiry')) {
    return { level: 'warning', text: 'Your license expires soon. Renew to avoid interruption.' }
  }
  if (w.includes('over_node_limit')) {
    return { level: 'warning', text: 'You have exceeded your licensed node limit — adding nodes is blocked.' }
  }
  return null
})

// New-release notice (platform admins). Dismissal is stored server-side against
// the version, not in localStorage: it must survive a browser change and it must
// come back when the *next* version lands.
const update = ref<UpdateInfo | null>(null)
const showUpdateBanner = computed(() => auth.isAdmin && update.value?.update_available === true)

async function loadUpdate() {
  if (!auth.isAdmin) return
  try {
    update.value = (await adminApi.getUpdate()).data.data
  } catch {
    // Non-critical; the panel works without it.
  }
}

async function dismissUpdate() {
  const version = update.value?.latest_version
  if (!version) return
  const previous = update.value
  update.value = { ...previous!, update_available: false } // optimistic
  try {
    await adminApi.dismissUpdate(version)
  } catch (e) {
    update.value = previous
    notify.apiError(e, 'Failed to dismiss the update notice')
  }
}

onMounted(async () => {
  document.addEventListener('click', closeMenus)
  if (!auth.user) await auth.fetchUser()
  if (auth.isAdmin) license.load().catch(() => { })
  loadUpdate()
  infoApi.get().then((res) => { docsEnabled.value = res.data.data.openapi_docs }).catch(() => { })
  try {
    await ws.fetchWorkspaces()
  } catch {
    notify.error('Failed to load workspaces')
  }
  // Only when the empty state is what the user will actually see; with a
  // workspace present the Dashboard fetches these itself.
  if (ws.workspaces.length === 0) await loadInvitations()
})
onBeforeUnmount(() => document.removeEventListener('click', closeMenus))
</script>

<template>
  <div class="layout" :class="{ 'sidebar-collapsed': sidebarCollapsed }">
    <!-- Desktop sidebar -->
    <aside class="sidebar">
      <div class="sidebar-header">
        <img src="/brand/miabi-mark-white.svg" alt="Miabi" class="sidebar-logo" @click="navigate('/')" />
        <span class="sidebar-brand-text" @click="navigate('/')">Miabi<span
            class="sidebar-brand-accent">.io</span></span>
        <button class="sidebar-collapse-btn" :title="sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'"
          :aria-label="sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'" @click="toggleSidebar">
          <!-- Panel toggle: a sidebar glyph whose inner chevron points the way it will move. -->
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
            stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2" />
            <path d="M9 3v18" />
            <path :d="sidebarCollapsed ? 'm14 9 3 3-3 3' : 'm16 15-3-3 3-3'" />
          </svg>
        </button>
      </div>

      <!-- Workspace switcher -->
      <div ref="wsSwitcherRef" class="ws-switcher">
        <div class="ws-switcher-toggle" @click="wsSwitcherOpen = !wsSwitcherOpen">
          <div class="ws-switcher-current">
            <div class="ws-avatar">{{ (ws.currentWorkspace?.display_name ||
              ws.currentWorkspace?.name)?.charAt(0)?.toUpperCase() || 'D' }}</div>
            <span v-if="!sidebarCollapsed" class="ws-switcher-name">{{ ws.contextLabel }}</span>
          </div>
          <span v-if="!sidebarCollapsed" class="mdi mdi-unfold-more-horizontal ws-switcher-chevron"></span>
        </div>
        <Transition name="dropdown">
          <div v-if="wsSwitcherOpen" class="ws-switcher-dropdown">
            <div v-for="w in ws.workspaces" :key="w.id" class="ws-switcher-option"
              :class="{ active: ws.currentWorkspaceId === w.id }" @click="switchWorkspace(w.id)">
              <div class="ws-avatar-sm">{{ (w.display_name || w.name).charAt(0).toUpperCase() }}</div>
              <span class="ws-switcher-option-name">
                {{ w.display_name || w.name }}
                <span class="ws-switcher-option-handle">{{ w.name }}</span>
              </span>
              <span v-if="w.role" class="ws-role-badge">{{ w.role }}</span>
            </div>
            <div v-if="!ws.workspaces.length" class="ws-switcher-empty">No workspaces yet</div>
            <div class="ws-switcher-divider"></div>
            <div class="ws-switcher-action" @click="createWorkspace">
              <span class="mdi mdi-plus"></span><span>Create workspace</span>
            </div>
            <div class="ws-switcher-action" @click="goWorkspaces">
              <span class="mdi mdi-briefcase-outline"></span><span>Manage workspaces…</span>
            </div>
          </div>
        </Transition>
      </div>

      <nav class="sidebar-nav">
        <div v-for="section in visibleSections" :key="section.id" class="nav-section">
          <button v-if="!sidebarCollapsed" class="nav-section-title" :aria-expanded="sectionOpen[section.id]"
            @click="toggleSection(section.id)">
            <span>{{ section.title }}</span>
            <span class="mdi mdi-chevron-down nav-section-chevron"
              :class="{ collapsed: !sectionOpen[section.id] }"></span>
          </button>
          <div v-show="sidebarCollapsed || sectionOpen[section.id]" class="nav-section-items">
            <template v-for="item in sectionItems(section)">
              <a v-if="item.external" :key="`ext-${item.name}`" class="nav-item" :href="docsUrl" target="_blank"
                rel="noopener noreferrer" :title="sidebarCollapsed ? item.name : ''" @click="mobileOpen = false">
                <span class="mdi nav-icon" :class="item.icon"></span>
                <span v-if="!sidebarCollapsed" class="nav-label">{{ item.name }}</span>
                <span v-if="!sidebarCollapsed" class="mdi mdi-open-in-new nav-external-icon"></span>
              </a>
              <router-link v-else :key="item.name" class="nav-item" :class="{ active: isItemActive(item) }"
                :title="sidebarCollapsed ? item.name : ''" :to="itemTo(item)" @click="mobileOpen = false">
                <span class="mdi nav-icon" :class="item.icon"></span>
                <span v-if="!sidebarCollapsed" class="nav-label">{{ item.name }}</span>
              </router-link>
            </template>
          </div>
        </div>
      </nav>
    </aside>

    <div class="main-wrapper">
      <header class="topbar">
        <div class="topbar-left">
          <button class="mobile-menu-btn" aria-label="Open menu" @click="mobileOpen = true">
            <span class="mdi mdi-menu"></span>
          </button>
        </div>
        <div class="topbar-right">
          <NotificationBell />
          <div class="user-menu" ref="userMenuRef">
 
          <div class="user-menu-trigger" @click="userMenuOpen = !userMenuOpen">
            <div class="user-avatar">{{ user?.name?.charAt(0)?.toUpperCase() || '?' }}</div>
            <div class="user-menu-info">
              <div class="user-name">{{ user?.name || 'User' }}</div>
              <div class="user-email">{{ user?.email || '' }}</div>
            </div>
            <span class="mdi mdi-chevron-down"></span>

            <Transition name="dropdown">
              <div v-if="userMenuOpen" class="user-dropdown">
                <div class="user-dropdown-header">
                  <span class="user-avatar user-avatar-lg">{{ user?.name?.charAt(0)?.toUpperCase() || '?' }}</span>
                  <div class="user-dropdown-info">
                    <div class="user-dropdown-name">{{ user?.name || 'User' }}</div>
                    <div class="user-dropdown-email">{{ user?.email || '' }}</div>
                  </div>
                </div>
                <div class="user-dropdown-divider"></div>
                <div class="user-dropdown-theme">
                  <div class="user-dropdown-theme-label">
                    <span class="mdi mdi-theme-light-dark"></span> Theme
                  </div>
                  <div class="theme-switcher">
                    <button v-for="m in themeModes" :key="m" :class="['theme-btn', { active: theme.mode === m }]"
                      :title="m.charAt(0).toUpperCase() + m.slice(1)"
                      :aria-label="m.charAt(0).toUpperCase() + m.slice(1)" @click.stop="theme.setMode(m)">
                      <span class="mdi"
                        :class="m === 'light' ? 'mdi-weather-sunny' : m === 'dark' ? 'mdi-weather-night' : 'mdi-monitor'"></span>
                    </button>
                  </div>
                </div>
                <div class="user-dropdown-divider"></div>
                <RouterLink v-if="auth.isAdmin" to="/admin/metrics" class="user-dropdown-item"
                  @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-shield-crown-outline"></span> Platform Admin
                </RouterLink>
                <RouterLink to="/account/profile" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-account-outline"></span> Profile
                </RouterLink>
                <RouterLink to="/account/security" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-shield-key-outline"></span> Security
                </RouterLink>
                <RouterLink to="/about" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-information-outline"></span> About
                </RouterLink>
                <div class="user-dropdown-divider"></div>
                <a href="/request-token" target="_blank" rel="noopener" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-console"></span> Copy login command
                </a>
                <div class="user-dropdown-divider"></div>
                <a class="user-dropdown-item user-dropdown-logout" @click.stop="logout">
                  <span class="mdi mdi-logout"></span> Sign out
                </a>
              </div>
            </Transition>
            </div>
          </div>
        </div>
      </header>

      <main class="main-content">
        <router-link v-if="licenseBanner" to="/admin/license" class="license-banner"
          :class="`license-banner-${licenseBanner.level}`">
          <span class="mdi mdi-alert-outline"></span>
          <span>{{ licenseBanner.text }}</span>
          <span class="license-banner-cta">Manage license →</span>
        </router-link>

        <!-- A newer Miabi release exists (admins). Links to the release notes; it
             never upgrades anything on its own. -->
        <div v-if="showUpdateBanner" class="update-banner">
          <span class="mdi mdi-arrow-up-bold-circle-outline update-banner-icon"></span>
          <span class="update-banner-text">
            <strong>Miabi {{ update?.latest_version }}</strong> is available — you're running
            {{ update?.current_version }}.
          </span>
          <a :href="update?.release_url" target="_blank" rel="noopener noreferrer" class="update-banner-cta">
            Release notes →
          </a>
          <button class="update-banner-dismiss" title="Dismiss until the next release"
            aria-label="Dismiss until the next release" @click="dismissUpdate">
            <span class="mdi mdi-close"></span>
          </button>
        </div>

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
                <button class="btn btn-primary btn-sm" :disabled="acceptingId === inv.id"
                  @click="acceptInvitation(inv)">
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
      </main>

      <footer class="main-footer">
        <div class="footer-left">
          <span>&copy; {{ currentYear }} Miabi Project</span>
          <span class="footer-sep">·</span>
          <span>Miabi</span>
        </div>
        <div class="footer-right">
          <RouterLink to="/about" class="footer-link">
            <span class="mdi mdi-information-outline"></span> About
          </RouterLink>
          <a href="https://github.com/miabi-io/miabi" target="_blank" rel="noopener noreferrer" class="footer-link">
            <span class="mdi mdi-github"></span> GitHub
          </a>
        </div>
      </footer>
    </div>

    <!-- Mobile overlay + sidebar -->
    <Transition name="overlay-fade">
      <div v-if="mobileOpen" class="sidebar-overlay" @click="mobileOpen = false" />
    </Transition>
    <Transition name="sidebar-slide">
      <aside v-if="mobileOpen" class="sidebar sidebar-mobile">
        <div class="sidebar-header">
          <img src="/brand/miabi-mark-white.svg" alt="Miabi" class="sidebar-logo" />
          <span class="sidebar-brand-text">Miabi<span class="sidebar-brand-accent">.io</span></span>
          <button class="sidebar-collapse-btn" aria-label="Close" @click="mobileOpen = false">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <div class="ws-switcher">
          <div class="ws-switcher-toggle" @click="wsSwitcherOpen = !wsSwitcherOpen">
            <div class="ws-switcher-current">
              <div class="ws-avatar">{{ (ws.currentWorkspace?.display_name ||
                ws.currentWorkspace?.name)?.charAt(0)?.toUpperCase() || 'D' }}</div>
              <span class="ws-switcher-name">{{ ws.contextLabel }}</span>
            </div>
            <span class="mdi mdi-unfold-more-horizontal ws-switcher-chevron"></span>
          </div>
          <Transition name="dropdown">
            <div v-if="wsSwitcherOpen" class="ws-switcher-dropdown">
              <div v-for="w in ws.workspaces" :key="w.id" class="ws-switcher-option"
                :class="{ active: ws.currentWorkspaceId === w.id }" @click="switchWorkspace(w.id)">
                <div class="ws-avatar-sm">{{ (w.display_name || w.name).charAt(0).toUpperCase() }}</div>
                <span class="ws-switcher-option-name">
                  {{ w.display_name || w.name }}
                  <span class="ws-switcher-option-handle">{{ w.name }}</span>
                </span>
                <span v-if="w.role" class="ws-role-badge">{{ w.role }}</span>
              </div>
              <div class="ws-switcher-divider"></div>
              <div class="ws-switcher-action" @click="createWorkspace">
                <span class="mdi mdi-plus"></span><span>Create workspace</span>
              </div>
              <div class="ws-switcher-action" @click="goWorkspaces">
                <span class="mdi mdi-briefcase-outline"></span><span>Manage workspaces…</span>
              </div>
            </div>
          </Transition>
        </div>
        <nav class="sidebar-nav">
          <div v-for="section in visibleSections" :key="section.id" class="nav-section">
            <button class="nav-section-title" @click="toggleSection(section.id)">
              <span>{{ section.title }}</span>
              <span class="mdi mdi-chevron-down nav-section-chevron"
                :class="{ collapsed: !sectionOpen[section.id] }"></span>
            </button>
            <div v-show="sectionOpen[section.id]" class="nav-section-items">
              <template v-for="item in sectionItems(section)">
                <a v-if="item.external" :key="`ext-${item.name}`" class="nav-item" :href="docsUrl" target="_blank"
                  rel="noopener noreferrer" @click="mobileOpen = false">
                  <span class="mdi nav-icon" :class="item.icon"></span>
                  <span class="nav-label">{{ item.name }}</span>
                  <span class="mdi mdi-open-in-new nav-external-icon"></span>
                </a>
                <router-link v-else :key="item.name" class="nav-item" :class="{ active: isItemActive(item) }"
                  :to="itemTo(item)" @click="mobileOpen = false">
                  <span class="mdi nav-icon" :class="item.icon"></span>
                  <span class="nav-label">{{ item.name }}</span>
                </router-link>
              </template>
            </div>
          </div>
        </nav>
      </aside>
    </Transition>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  min-height: 100vh;
  background: var(--bg-secondary);
}

/* ─── Pending invitations in the no-workspace empty state ─── */
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

/* ─── Sidebar ─── */
.sidebar {
  position: fixed;
  top: 0;
  left: 0;
  bottom: 0;
  width: 240px;
  background: var(--bg-sidebar);
  display: flex;
  flex-direction: column;
  z-index: 40;
  transition: width var(--transition-slow);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) {
  width: 64px;
}

.sidebar-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 16px 14px 12px;
  flex-shrink: 0;
  position: relative;
}

.sidebar-logo {
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  cursor: pointer;
}

.sidebar-brand-text {
  font-size: 19px;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: #fff;
  white-space: nowrap;
  overflow: hidden;
  cursor: pointer;
  transition: opacity var(--transition-slow);
}

/* Two-tone wordmark: the trailing .io carries the brand accent. */
.sidebar-brand-accent {
  color: var(--primary-400);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .sidebar-brand-text {
  opacity: 0;
  width: 0;
}

.sidebar-collapse-btn {
  background: none;
  border: none;
  color: var(--sidebar-text);
  cursor: pointer;
  padding: 4px;
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
  transition: color var(--transition), background var(--transition);
  position: absolute;
  top: 18px;
  right: 10px;
}

.sidebar-collapse-btn:hover {
  color: #fff;
  background: var(--sidebar-hover);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .sidebar-collapse-btn {
  right: -14px;
  top: 20px;
  background: var(--bg-sidebar);
  border: 1px solid var(--sidebar-border);
  z-index: 50;
}

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

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .ws-switcher-toggle {
  justify-content: center;
  padding: 8px;
  border-color: transparent;
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

/* ─── Navigation ─── */
.sidebar-nav {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 8px;
}

.nav-section+.nav-section {
  margin-top: 14px;
}

.nav-section-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  background: none;
  border: none;
  cursor: pointer;
  font-family: inherit;
  font-size: 10.5px;
  font-weight: 600;
  color: var(--sidebar-text);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  padding: 4px 12px 6px;
  opacity: 0.6;
  transition: opacity var(--transition);
}

.nav-section-title:hover {
  opacity: 1;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-section-title {
  display: none;
}

.nav-section-chevron {
  font-size: 14px;
  transition: transform var(--transition);
}

.nav-section-chevron.collapsed {
  transform: rotate(-90deg);
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 12px;
  border-radius: var(--radius);
  color: var(--sidebar-text);
  font-size: 13px;
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
  text-decoration: none;
  white-space: nowrap;
  overflow: hidden;
  position: relative;
}

.nav-item:hover {
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
}

.nav-item.active {
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
  font-weight: 500;
}

.nav-item.active::before {
  content: '';
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 20px;
  background: var(--primary-500);
  border-radius: 0 3px 3px 0;
}

.nav-icon {
  flex-shrink: 0;
  font-size: 18px;
  opacity: 0.8;
}

.nav-external-icon {
  margin-left: auto;
  font-size: 13px;
  opacity: 0.5;
}

.nav-item.active .nav-icon {
  opacity: 1;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-item {
  justify-content: center;
  padding: 9px;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-label {
  display: none;
}

/* ─── Main wrapper ─── */
.main-wrapper {
  flex: 1;
  margin-left: 240px;
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  transition: margin-left var(--transition-slow);
}

.sidebar-collapsed .main-wrapper {
  margin-left: 64px;
}

/* ─── Topbar ─── */
.topbar {
  position: sticky;
  top: 0;
  z-index: 30;
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 56px;
  padding: 0 24px;
  background: var(--bg-primary);
  border-bottom: 1px solid var(--border-primary);
  transition: background var(--transition-slow), border-color var(--transition-slow);
}

.topbar-left {
  display: flex;
  align-items: center;
}

.mobile-menu-btn {
  display: none;
  align-items: center;
  justify-content: center;
  background: none;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  font-size: 22px;
  padding: 6px;
  border-radius: var(--radius-sm);
  transition: color var(--transition), background var(--transition);
}

.mobile-menu-btn:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-left: auto;
}

/* ─── User menu ─── */
/* .user-menu {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 5px 10px;
  border-radius: var(--radius);
  cursor: pointer;
  transition: background var(--transition);
  position: relative;
  user-select: none;
  color: var(--text-secondary);
}

.user-menu:hover {
  background: var(--bg-hover);
} */
.user-menu-trigger {
  display: flex;
  align-items: center;
  gap: 8px;
  background: none;
  border: 1px solid transparent;
  padding: 5px 10px 5px 5px;
  border-radius: var(--radius);
  cursor: pointer;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: 14px;
  transition: background var(--transition), border-color var(--transition);
}

.user-menu-trigger:hover {
  background: var(--bg-hover);
  border-color: var(--border-primary);
}
.user-avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: var(--primary-600);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  flex-shrink: 0;
}

.user-avatar-lg {
  width: 38px;
  height: 38px;
  font-size: 15px;
}

.user-menu-info {
  overflow: hidden;
}

.user-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 140px;
}

.user-email {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 140px;
}

.user-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  width: 260px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  z-index: 50;
  overflow: hidden;
}

.user-dropdown-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
}

.user-dropdown-info {
  overflow: hidden;
}

.user-dropdown-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-dropdown-email {
  font-size: 12px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-dropdown-divider {
  height: 1px;
  background: var(--border-primary);
}

.user-dropdown-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 16px;
  font-size: 14px;
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
  text-decoration: none;
}

.user-dropdown-item .mdi {
  font-size: 17px;
}

.user-dropdown-item:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.user-dropdown-logout {
  color: var(--danger-600);
}

.user-dropdown-logout:hover {
  background: var(--danger-50);
  color: var(--danger-700);
}

.user-dropdown-theme {
  padding: 10px 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.user-dropdown-theme-label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-muted);
  font-weight: 500;
}

.theme-switcher {
  display: flex;
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  padding: 2px;
  gap: 2px;
}

.theme-btn {
  padding: 5px 9px;
  border: none;
  border-radius: 4px;
  font-size: 13px;
  cursor: pointer;
  color: var(--text-tertiary);
  background: transparent;
  display: flex;
  align-items: center;
  transition: all var(--transition);
}

.theme-btn:hover {
  color: var(--text-primary);
}

.theme-btn.active {
  background: var(--bg-primary);
  color: var(--text-primary);
  box-shadow: var(--shadow-sm);
}

/* ─── Main content ─── */
.main-content {
  flex: 1;
  padding: 28px;
}

/* ─── License banner ─── */
.license-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 16px;
  margin-bottom: 20px;
  border-radius: 8px;
  border: 1px solid;
  font-size: 13px;
  font-weight: 500;
  text-decoration: none;
}

.license-banner .mdi {
  font-size: 18px;
  flex-shrink: 0;
}

.license-banner-cta {
  margin-left: auto;
  white-space: nowrap;
  font-weight: 600;
  opacity: 0.85;
}

.license-banner-warning {
  background: var(--warning-bg, rgba(245, 158, 11, 0.12));
  border-color: var(--warning, #d97706);
  color: var(--warning, #b45309);
}

.license-banner-danger {
  background: var(--danger-bg, rgba(239, 68, 68, 0.12));
  border-color: var(--danger, #dc2626);
  color: var(--danger, #b91c1c);
}

/* ─── Community Edition banner ─── */
/* ─── New-release notice (admins) ─── */
.update-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  margin-bottom: 20px;
  border-radius: 10px;
  border: 1px solid var(--success-500, #16a34a);
  background: var(--success-50, rgba(22, 163, 74, 0.08));
  color: var(--text-secondary, var(--text-muted));
  font-size: 13px;
  line-height: 1.45;
}

[data-theme="dark"] .update-banner {
  border: 1px solid var(--success-800, #16a34a4c);
}

.update-banner-icon {
  font-size: 20px;
  flex-shrink: 0;
  color: var(--success-500, #16a34a);
}

.update-banner-text strong {
  color: var(--text-primary);
  font-weight: 600;
}

.update-banner-cta {
  margin-left: auto;
  white-space: nowrap;
  font-weight: 600;
  text-decoration: none;
  color: var(--success-500, #16a34a);
}

.update-banner-cta:hover {
  text-decoration: underline;
}

.update-banner-dismiss {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  padding: 2px;
  border: none;
  border-radius: 4px;
  background: none;
  color: var(--text-muted);
  cursor: pointer;
}

.update-banner-dismiss:hover {
  color: var(--text-primary);
}

.ce-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  margin-bottom: 20px;
  border-radius: 10px;
  border: 1px solid var(--accent, #6366f1);
  background: var(--accent-bg, rgba(99, 102, 241, 0.08));
  color: var(--text-secondary, var(--text-muted));
  font-size: 13px;
  line-height: 1.45;
}

[data-theme="dark"] .ce-banner {
  border: 1px solid var(--accent, #6365f152);
}

.ce-banner-icon {
  font-size: 20px;
  flex-shrink: 0;
  color: var(--accent, #6366f1);
}

.ce-banner-text {
  flex: 1;
  min-width: 0;
}

.ce-banner-text strong {
  color: var(--text-primary);
  font-weight: 600;
}

.ce-banner-cta {
  margin-left: auto;
  white-space: nowrap;
  font-weight: 600;
  text-decoration: none;
  color: var(--accent, #6366f1);
}

.ce-banner-cta:hover {
  text-decoration: underline;
}

.ce-banner-dismiss {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  padding: 4px;
  border: none;
  border-radius: 4px;
  background: none;
  color: var(--text-muted);
  cursor: pointer;
}

.ce-banner-dismiss:hover {
  color: var(--text-primary);
  background: var(--bg-tertiary, rgba(127, 127, 127, 0.12));
}

/* ─── Footer ─── */
.main-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 28px;
  border-top: 1px solid var(--border-primary);
  font-size: 13px;
  color: var(--text-muted);
  background: var(--bg-primary);
}

.footer-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

.footer-sep {
  opacity: 0.5;
}

.footer-link {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
  transition: color var(--transition);
}

.footer-link:hover {
  color: var(--text-primary);
}

/* ─── Mobile ─── */
.sidebar-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  z-index: 35;
  backdrop-filter: blur(4px);
}

.sidebar-mobile {
  z-index: 45;
  width: 240px;
}

/* ─── Transitions ─── */
.dropdown-enter-active,
.dropdown-leave-active {
  transition: opacity 150ms ease, transform 150ms ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

.overlay-fade-enter-active,
.overlay-fade-leave-active {
  transition: opacity 180ms ease;
}

.overlay-fade-enter-from,
.overlay-fade-leave-to {
  opacity: 0;
}

.sidebar-slide-enter-active,
.sidebar-slide-leave-active {
  transition: transform 200ms ease;
}

.sidebar-slide-enter-from,
.sidebar-slide-leave-to {
  transform: translateX(-100%);
}

/* ─── Responsive ─── */
@media (max-width: 1024px) {
  .sidebar:not(.sidebar-mobile) {
    display: none;
  }

  .main-wrapper {
    margin-left: 0 !important;
  }

  .mobile-menu-btn {
    display: flex;
  }
}

@media (max-width: 639px) {
  .main-content {
    padding: 20px 16px;
  }

  .topbar {
    padding: 0 16px;
  }

  .main-footer {
    padding: 14px 16px;
    flex-direction: column;
    gap: 8px;
    text-align: center;
  }

  .user-name,
  .user-email {
    display: none;
  }
}

@media (max-width: 639px) {

  .ce-banner,
  .update-banner {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 8px 12px;
    padding: 10px;
    margin-left: 2px;
    margin-right: 2px;
  }

  .ce-banner-icon,
  .update-banner-icon {
    grid-column: 1;
    grid-row: 1;
    align-self: start;
    margin-top: 2px;
  }

  .ce-banner-text,
  .update-banner-text {
    grid-column: 2;
    grid-row: 1;
  }

  .ce-banner-cta,
  .update-banner-cta {
    grid-column: 2;
    grid-row: 2;
    margin-left: 0;
    align-self: start;
  }

  .ce-banner,
  .update-banner {
    position: relative;
  }

  .ce-banner-dismiss,
  .update-banner-dismiss {
    position: absolute;
    top: 8px;
    right: 8px;
  }

  .ce-banner-text,
  .update-banner-text {
    padding-right: 24px;
  }
}
</style>
