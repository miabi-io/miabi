<script setup lang="ts">
import { useI18n } from 'vue-i18n'
// Workspace dashboard
import { computed, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { useAuthStore } from '@/stores/auth'
import { workspaceApi } from '@/api/workspaces'
import { stackApi } from '@/api/stacks'
import { domainApi } from '@/api/domains'
import GettingStarted from '@/components/GettingStarted.vue'
import AnalyticsOverview from './AnalyticsOverview.vue'
import AppsTable from './AppsTable.vue'
import EventsTimeline from './EventsTimeline.vue'
import InvitationsCard from './InvitationsCard.vue'
import OverviewStats from './OverviewStats.vue'
import QuickActions from './QuickActions.vue'
import ResourcesCard from './ResourcesCard.vue'
import StacksTable from './StacksTable.vue'
import { useLiveUsage } from './useLiveUsage'
import { useWorkspaceStream } from './useWorkspaceStream'
import type { AppEvent, Overview, PendingInvitation, RecentEvent, Stack } from '@/api/types'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const auth = useAuthStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const overview = ref<Overview | null>(null)
const stacks = ref<Stack[]>([])
const domainCount = ref(0)
const loading = ref(false)

// Destructured so the template binds the refs directly (auto-unwrapped) rather
// than reaching through an object for every read.
const { sample: usage, cpuSeries, memSeries, start: startUsage } = useLiveUsage()
const { connected: streamLive, freshEventId, open: openStream, FEED_CAP } = useWorkspaceStream(reconcile)

// Getting-started checklist: each step's done state is derived from real
// resources, so it ticks itself. Steps are monotonic (a later one can't complete
// before an earlier one), and the platform's own "Miabi System" workspace is
// ignored for the freshly-seeded admin.
const showOnboarding = computed(() => !auth.user?.onboarding_dismissed)
const hasWorkspace = computed(() => ws.workspaces.some((w) => !w.system))
const hasApp = computed(() => hasWorkspace.value && (overview.value?.total_apps ?? 0) > 0)
const hasDomain = computed(() => hasApp.value && domainCount.value > 0)

// Time-of-day greeting with the user's first name.
const greeting = computed(() => {
  const h = new Date().getHours()
  const part = h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
  const name = auth.user?.name?.trim().split(' ')[0]
  return name ? `${part}, ${name}` : part
})

// At-a-glance health derived from the overview counts. Failures get a banner of
// their own below, so this pill stays quiet and doesn't compete with it.
const health = computed(() => {
  const o = overview.value
  if (!o) return { tone: 'neutral', icon: 'mdi-circle-outline', text: t('dashboard.health.loading') }
  if (o.failed > 0) {
    return { tone: 'danger', icon: 'mdi-alert-circle', text: t('dashboard.health.failing', o.failed) }
  }
  if (o.total_apps === 0) return { tone: 'neutral', icon: 'mdi-information-outline', text: t('dashboard.apps.empty') }
  return { tone: 'success', icon: 'mdi-check-circle', text: t('dashboard.health.allHealthy') }
})

const failedCount = computed(() => overview.value?.failed ?? 0)

const invitations = ref<PendingInvitation[]>([])
const acceptingId = ref<number | null>(null)

async function loadInvitations() {
  try {
    invitations.value = (await workspaceApi.myInvitations()).data.data ?? []
  } catch {
    // Non-critical; the dashboard works without it.
  }
}

async function accept(inv: PendingInvitation) {
  acceptingId.value = inv.id
  try {
    await workspaceApi.acceptInvitation(inv.id)
    notify.success(t('notify.workspaces.joined', { name: inv.workspace_name }))
    await ws.fetchWorkspaces()
    ws.setWorkspace(inv.workspace_id)
    await loadInvitations()
  } catch (e) {
    notify.apiError(e, 'Failed to accept invitation')
  } finally {
    acceptingId.value = null
  }
}

onMounted(loadInvitations)

async function load(id: number | null) {
  overview.value = null
  stacks.value = []
  domainCount.value = 0
  if (!id) return
  loading.value = true
  try {
    overview.value = (await workspaceApi.overview(id)).data.data
    stacks.value = (await stackApi.list(id)).data.data ?? []
    // Domain count drives the onboarding checklist; only fetched when the card
    // is still showing, to avoid an extra request for settled users.
    if (showOnboarding.value) {
      domainCount.value = (await domainApi.list(id)).data.data?.length ?? 0
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// reconcile re-reads the counts without tearing the page down to a skeleton, so
// health and per-app status stay truthful as events arrive.
async function reconcile(id: number) {
  try {
    const [ov, st] = await Promise.all([workspaceApi.overview(id), stackApi.list(id)])
    overview.value = ov.data.data
    stacks.value = st.data.data ?? []
  } catch {
    // Transient; the next event (or the stream reconnect) will retry.
  }
}

// A streamed event lands in the timeline immediately, enriched with its app's
// name from the overview we already hold.
function spliceEvent(e: AppEvent) {
  const ov = overview.value
  if (!ov) return
  const app = ov.apps?.find((a) => a.id === e.application_id)
  const enriched: RecentEvent = { ...e, app_name: app?.name ?? '', app_display_name: app?.display_name ?? '' }
  const existing = ov.recent_events ?? []
  ov.recent_events = [enriched, ...existing.filter((x) => x.id !== enriched.id)].slice(0, FEED_CAP)
}

watch(
  currentWorkspaceId,
  (id) => {
    load(id)
    openStream(id, spliceEvent)
    startUsage(id)
  },
  { immediate: true },
)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ greeting }}</h1>
        <p class="subtitle">
          {{ $t('dashboard.overviewOf') }} <strong>{{ ws.contextLabel }}</strong>
          <span v-if="overview" class="health-pill" :class="`health-${health.tone}`">
            <span class="mdi" :class="health.icon"></span> {{ health.text }}
          </span>
          <span
            v-if="overview"
            class="live-pill"
            :class="{ 'is-live': streamLive }"
            :title="streamLive ? t('dashboard.stream.live') : t('dashboard.stream.reconnecting')"
          >
            <span class="live-dot"></span>
          </span>
        </p>
      </div>
      <div class="page-header-actions">
        <button v-if="ws.canEdit" class="btn btn-primary" @click="router.push('/apps')">
          <span class="mdi mdi-plus"></span> {{ $t('dashboard.newApplication') }}
        </button>
      </div>
    </div>

    <!-- Failures get one actionable banner at the top rather than a colour
         somewhere in a stat tile. -->
    <div v-if="failedCount > 0" class="alert-banner" role="alert">
      <span class="mdi mdi-alert-circle"></span>
      <span class="ab-text">
        <strong>{{ $t('dashboard.banner.failing', failedCount) }}</strong>
        {{ $t('dashboard.banner.checkLogs') }}
      </span>
      <button class="btn btn-sm btn-danger" @click="router.push('/apps')">{{ $t('dashboard.viewApplications') }}</button>
    </div>

    <GettingStarted v-if="showOnboarding" :has-workspace="hasWorkspace" :has-app="hasApp" :has-domain="hasDomain" />

    <InvitationsCard
      v-if="invitations.length"
      :invitations="invitations"
      :accepting-id="acceptingId"
      @accept="accept"
    />

    <QuickActions v-if="ws.isWorkspaceContext && ws.canEdit" />

    <div v-if="loading && !overview" class="stats-grid">
      <div v-for="i in 4" :key="i" class="stat-card">
        <span class="skeleton skeleton-text" style="width: 40%"></span>
        <span class="skeleton" style="height: 28px; width: 50%; margin-top: 10px"></span>
      </div>
    </div>

    <template v-else-if="overview">
      <OverviewStats :overview="overview" />

      <!-- What the workspace is serving, then what it is spending. -->
      <AnalyticsOverview v-if="currentWorkspaceId" :workspace-id="currentWorkspaceId" />

      <ResourcesCard
        v-if="overview.total_apps > 0"
        :sample="usage"
        :cpu-series="cpuSeries"
        :mem-series="memSeries"
      />

      <!-- Inventory keeps the full width on wide screens; activity rides
           alongside it instead of adding another screen of scroll. -->
      <div class="dash-columns">
        <div class="dash-main">
          <AppsTable :apps="overview.apps" :can-edit="ws.canEdit" />
          <StacksTable v-if="stacks.length" :stacks="stacks" />
        </div>
        <div class="dash-side">
          <EventsTimeline :events="overview.recent_events" :fresh-id="freshEventId" />
        </div>
      </div>
    </template>

    <div v-else-if="ws.loaded && !currentWorkspaceId" class="card">
      <div class="empty-state">
        <span class="mdi mdi-briefcase-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('dashboard.noWorkspace') }}</h3>
        <p v-if="invitations.length">{{ $t('dashboard.acceptInvitation') }}</p>
        <p v-else>{{ $t('dashboard.createToStart') }}</p>
        <button class="btn btn-primary mt-4" @click="router.push('/workspaces')">{{ $t('switcher.create') }}</button>
      </div>
    </div>

    <div v-else class="loading-page"><span class="spinner"></span></div>
  </div>
</template>

<style scoped>
.subtitle {
  font-size: 13px; color: var(--text-muted); margin-top: 4px;
  display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-width: 0;
}
.subtitle strong { color: var(--text-secondary); word-break: break-word; }

.health-pill {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 3px 10px; border-radius: 9999px; font-size: 12px; font-weight: 600;
}
.health-pill .mdi { font-size: 14px; }
.health-success { background: var(--success-50); color: var(--success-600); }
.health-danger { background: var(--danger-50); color: var(--danger-600); }
.health-neutral { background: var(--bg-tertiary); color: var(--text-muted); }

/* Live indicator: a small connected/animated dot next to the health pill. */
.live-pill { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; font-weight: 600; color: var(--text-muted); }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--text-muted); flex-shrink: 0; }
.live-pill.is-live { color: var(--success-600); }
.live-pill.is-live .live-dot { background: var(--success-500, #16a34a); animation: live-blink 1.6s ease-in-out infinite; }
@keyframes live-blink {
  0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(22, 163, 74, 0.5); }
  50% { opacity: 0.5; box-shadow: 0 0 0 4px rgba(22, 163, 74, 0); }
}
@media (prefers-reduced-motion: reduce) {
  .live-pill.is-live .live-dot { animation: none; }
}

.alert-banner {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  padding: 12px 16px; margin-bottom: 20px;
  border: 1px solid var(--danger-500); border-radius: var(--radius-lg);
  background: var(--danger-50); color: var(--danger-600);
}
.alert-banner .mdi { font-size: 20px; flex-shrink: 0; }
.ab-text { flex: 1; min-width: 200px; font-size: 13px; color: var(--text-secondary); }
.ab-text strong { color: var(--danger-600); }

/* Inventory + activity. One column until there's room for the rail. */
.dash-columns { display: grid; grid-template-columns: 1fr; gap: 20px; align-items: start; }
.dash-main, .dash-side { display: flex; flex-direction: column; gap: 20px; min-width: 0; }
@media (min-width: 1180px) {
  .dash-columns { grid-template-columns: minmax(0, 1.9fr) minmax(320px, 1fr); }
}
</style>
