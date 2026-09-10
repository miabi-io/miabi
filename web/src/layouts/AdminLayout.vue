<script setup lang="ts">
import { ref, computed, onMounted, watch, onBeforeUnmount } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import AnnouncementBanner from '@/components/AnnouncementBanner.vue'
import ConsoleShell from './ConsoleShell.vue'
import { adminNavSections } from '@/data/adminNav'
import { adminApi } from '@/api/admin'
import { ADMIN_HOME } from '@/data/console'
import type { UpdateInfo } from '@/api/types'

// The platform console. It deliberately has no no-workspace empty state: platform
// administration is not scoped to a workspace, and gating it on owning one locked
// a fresh install's operator out of every admin page, licence installation
// included.
const route = useRoute()
const auth = useAuthStore()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const license = useLicenseStore()

// License banner: admins see a warning when the license is in grace, expired,
// nearing expiry, or over the node limit. Driven by the cached entitlements.
const licenseBanner = computed(() => {
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

// New-release notice. Dismissal is stored server-side against the version, not in
// localStorage: it must survive a browser change and it must come back when the
// *next* version lands.
const update = ref<UpdateInfo | null>(null)
const showUpdateBanner = computed(() => update.value?.update_available === true)

async function loadUpdate() {
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

// A background tab should say which console it is, so an admin returning to a
// window does not act on the wrong one.
function applyTitlePrefix() {
  const base = (route.meta.title as string | undefined) ?? 'Miabi'
  document.title = `Admin · ${base} — Miabi`
}
watch(() => route.fullPath, applyTitlePrefix)

onMounted(async () => {
  if (!auth.user || !auth.user.preferences) await auth.fetchUser()
  license.load().catch(() => { })
  loadUpdate()
  applyTitlePrefix()
  // The switcher lists workspaces as the way back out, so they are still needed
  // here — but nothing in this console is gated on there being any.
  ws.fetchWorkspaces().catch(() => { })
})
onBeforeUnmount(() => {
  // Leaving the console hands the title back to the router's own handling.
  const base = (route.meta.title as string | undefined) ?? 'Miabi'
  document.title = `${base} — Miabi`
})
</script>

<template>
  <ConsoleShell :sections="adminNavSections" console="admin" section-state-key="mb_admin_nav_sections"
    :home="ADMIN_HOME">
    <template #banners>
      <AnnouncementBanner />

      <router-link v-if="licenseBanner" to="/admin/license" class="license-banner"
        :class="`license-banner-${licenseBanner.level}`">
        <span class="mdi mdi-alert-outline"></span>
        <span>{{ licenseBanner.text }}</span>
        <span class="license-banner-cta">Manage license →</span>
      </router-link>

      <!-- A newer Miabi release exists. Links to the release notes; it never
           upgrades anything on its own. -->
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
    </template>

    <router-view />
  </ConsoleShell>
</template>

<style scoped>
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

@media (max-width: 639px) {
  .update-banner {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 8px 12px;
    padding: 10px;
    margin-left: 2px;
    margin-right: 2px;
    position: relative;
  }

  .update-banner-icon {
    grid-column: 1;
    grid-row: 1;
    align-self: start;
    margin-top: 2px;
  }

  .update-banner-text {
    grid-column: 2;
    grid-row: 1;
    padding-right: 24px;
  }

  .update-banner-cta {
    grid-column: 2;
    grid-row: 2;
    margin-left: 0;
    align-self: start;
  }

  .update-banner-dismiss {
    position: absolute;
    top: 8px;
    right: 8px;
  }
}
</style>
