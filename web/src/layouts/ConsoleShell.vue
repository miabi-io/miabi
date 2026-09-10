<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useWorkspaceStore } from '@/stores/workspace'
import NotificationBell from '@/components/NotificationBell.vue'
import CommandPalette from '@/components/CommandPalette.vue'
import ContextSwitcher from '@/components/ContextSwitcher.vue'
import { type NavItem, type NavSection } from '@/data/nav'
import { infoApi } from '@/api/info'
import { ADMIN_HOME } from '@/data/console'

// The frame both consoles share. What differs is the navigation it is handed and
// which identity the context switcher wears — everything else (topbar, user menu,
// mobile drawer, palette, footer) is the same product chrome, and was previously
// duplicated between the desktop and mobile sidebars of one 1,700-line file.
const props = defineProps<{
  sections: NavSection[]
  console: 'workspace' | 'admin'
  // Section state is remembered per console: the admin sections are not the
  // workspace ones, so one shared key would have them fight over the same names.
  sectionStateKey: string
  home: string
}>()

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const theme = useThemeStore()
const ws = useWorkspaceStore()

const sidebarCollapsed = ref(localStorage.getItem('mb_sidebar_collapsed') === 'true')
const mobileOpen = ref(false)
const userMenuOpen = ref(false)
const themeModes = ['light', 'dark', 'system'] as const

const user = computed(() => auth.user)
const currentYear = new Date().getFullYear()

// The interactive API reference (/docs) is served at the server root, a sibling
// of the API base prefix. Hidden until /info confirms it's enabled.
const docsEnabled = ref(false)
const docsUrl = ((import.meta.env.VITE_API_URL as string) || '/api/v1').replace(/\/api\/v1\/?$/, '') + '/docs'

function loadSectionState(): Record<string, boolean> {
  const defaults: Record<string, boolean> = {}
  props.sections.forEach((s) => (defaults[s.id] = s.defaultOpen !== false))
  try {
    return { ...defaults, ...JSON.parse(localStorage.getItem(props.sectionStateKey) || '{}') }
  } catch {
    return defaults
  }
}
const sectionOpen = ref<Record<string, boolean>>(loadSectionState())
function toggleSection(id: string) {
  sectionOpen.value[id] = !sectionOpen.value[id]
  localStorage.setItem(props.sectionStateKey, JSON.stringify(sectionOpen.value))
}

function sectionItems(section: NavSection): NavItem[] {
  return section.items.filter((item) => {
    if (item.requiresWorkspace && !ws.isWorkspaceContext) return false
    if (item.requiresWorkspaceAdmin && !(ws.isWorkspaceContext && ws.isWorkspaceAdmin)) return false
    if (item.requiresDocs && !docsEnabled.value) return false
    return true
  })
}
const visibleSections = computed(() => props.sections.filter((s) => sectionItems(s).length > 0))

function itemTo(item: NavItem): string {
  if (item.workspaceTab) return `/workspaces/${ws.currentWorkspaceId}?tab=${item.workspaceTab}`
  return item.path
}

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/'
  // The admin dashboard also answers /admin, so it stays lit on the bare path.
  if (path === '/admin/metrics' && route.path === ADMIN_HOME) return true
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

function logout() {
  auth.logout()
  ws.clear()
  router.push('/login')
}

// Close on any outside click. Match by class (not a template ref): the switcher is
// rendered twice — desktop sidebar and mobile sidebar — so one ref can't cover both.
function closeMenus(e: MouseEvent) {
  const target = e.target as Element
  if (userMenuOpen.value && !target.closest?.('.user-menu')) userMenuOpen.value = false
}

const paletteOpen = ref(false)
const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const paletteHint = computed(() => (isMac ? '⌘K' : 'Ctrl K'))

function onPaletteShortcut(event: KeyboardEvent) {
  if (event.key !== 'k' && event.key !== 'K') return
  if (!event.metaKey && !event.ctrlKey) return
  event.preventDefault()
  paletteOpen.value = !paletteOpen.value
}

onMounted(() => {
  document.addEventListener('click', closeMenus)
  document.addEventListener('keydown', onPaletteShortcut)
  theme.adopt(auth.user?.preferences?.theme, auth.user?.preferences?.accent)
  infoApi.get().then((res) => { docsEnabled.value = res.data.data.openapi_docs }).catch(() => { })
})
onBeforeUnmount(() => {
  document.removeEventListener('click', closeMenus)
  document.removeEventListener('keydown', onPaletteShortcut)
})
</script>

<template>
  <div class="layout" :class="[{ 'sidebar-collapsed': sidebarCollapsed }, `console-${props.console}`]">
    <!-- Desktop sidebar -->
    <aside class="sidebar">
      <div class="sidebar-header">
        <img src="/brand/miabi-mark-white.svg" alt="Miabi" class="sidebar-logo" @click="navigate(props.home)" />
        <span class="sidebar-brand-text" @click="navigate(props.home)">Miabi<span
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

      <ContextSwitcher :mode="props.console" :collapsed="sidebarCollapsed" />

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
          <button class="topbar-search" type="button" aria-label="Search" @click="paletteOpen = true">
            <span class="mdi mdi-magnify"></span>
            <span class="topbar-search-text">Search or jump to…</span>
            <kbd class="topbar-search-kbd">{{ paletteHint }}</kbd>
          </button>
        </div>
        <div class="topbar-right">
          <button class="topbar-search-icon" type="button" aria-label="Search" @click="paletteOpen = true">
            <span class="mdi mdi-magnify"></span>
          </button>
          <NotificationBell />
          <div class="user-menu">
            <div class="user-menu-trigger" @click="userMenuOpen = !userMenuOpen">
              <div class="user-avatar">{{ user?.name?.charAt(0)?.toUpperCase() || '?' }}</div>
              <div class="user-menu-info">
                <div class="user-name">{{ user?.name || 'User' }}</div>
                <div class="user-email">{{ user?.email || '' }}</div>
              </div>
              <span class="mdi mdi-chevron-down"></span>
            </div>

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
                <RouterLink to="/account/profile" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-account-outline"></span> Profile
                </RouterLink>
                <RouterLink to="/account/preferences" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-tune-variant"></span> Preferences
                </RouterLink>
                <RouterLink to="/account/security" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-shield-key-outline"></span> Security
                </RouterLink>
                <RouterLink to="/about" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-information-outline"></span> About
                </RouterLink>
                <div class="user-dropdown-divider"></div>
                <RouterLink to="/account/cli" class="user-dropdown-item" @click.stop="userMenuOpen = false">
                  <span class="mdi mdi-console"></span> CLI access
                </RouterLink>
                <div class="user-dropdown-divider"></div>
                <a class="user-dropdown-item user-dropdown-logout" @click.stop="logout">
                  <span class="mdi mdi-logout"></span> Sign out
                </a>
              </div>
            </Transition>
          </div>
        </div>
      </header>

      <main class="main-content">
        <slot name="banners" />
        <slot />
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
        <ContextSwitcher :mode="props.console" @navigate="mobileOpen = false" />
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

    <CommandPalette v-model:open="paletteOpen" :docs-enabled="docsEnabled" :docs-url="docsUrl" />
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  min-height: 100vh;
  background: var(--bg-secondary);
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

/* The admin console's sidebar carries a warm cast so the console you are in is
   readable at a glance. Deleting an app and deleting a user look identical
   otherwise, and only one of them is recoverable. */
.console-admin .sidebar {
  background:
    linear-gradient(to bottom,
      color-mix(in srgb, var(--warning-600, #d97706) 26%, transparent),
      color-mix(in srgb, var(--warning-600, #d97706) 8%, transparent) 45%,
      transparent 75%),
    var(--bg-sidebar);
  box-shadow: inset -2px 0 0 color-mix(in srgb, var(--warning-600, #d97706) 45%, transparent);
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
  /* Allow this flex item to shrink below its content's intrinsic width.
     Without it, a wide table forces the whole wrapper (topbar included) past
     the viewport edge and the page scrolls sideways instead of the table
     scrolling inside its own wrapper. */
  min-width: 0;
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
  gap: 12px;
  min-width: 0;
  flex: 1;
}

.topbar-search {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  max-width: 380px;
  height: 34px;
  padding: 0 8px 0 10px;
  background: var(--bg-secondary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  color: var(--text-tertiary);
  font-size: 13px;
  cursor: pointer;
  transition: border-color var(--transition), background var(--transition), color var(--transition);
}

.topbar-search:hover {
  border-color: var(--border-secondary, var(--border-primary));
  background: var(--bg-hover);
  color: var(--text-secondary);
}

.topbar-search .mdi {
  font-size: 17px;
}

.topbar-search-text {
  flex: 1;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.topbar-search-kbd {
  border: 1px solid var(--border-primary);
  background: var(--bg-primary);
  border-radius: 4px;
  padding: 1px 6px;
  font-size: 11px;
  font-family: inherit;
  color: var(--text-muted);
  flex-shrink: 0;
}

.topbar-search-icon {
  display: none;
  align-items: center;
  justify-content: center;
  background: none;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  font-size: 20px;
  padding: 6px;
  border-radius: var(--radius-sm);
  transition: color var(--transition), background var(--transition);
}

.topbar-search-icon:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
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
/* Positioning context for the absolutely-positioned dropdown below. Without
   this the dropdown anchors to the sticky topbar and drifts off the trigger. */
.user-menu {
  position: relative;
}

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
  min-width: 0;
  padding: 28px;
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

@media (max-width: 767px) {
  .topbar-search {
    display: none;
  }

  .topbar-search-icon {
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
</style>
