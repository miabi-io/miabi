<script setup lang="ts">
// The workspace activity feed. Events stream in over SSE, so the newest row is
// briefly highlighted to show the page is live rather than stale.
import { useRouter } from 'vue-router'
import type { RecentEvent } from '@/api/types'
import { relTime } from './format'
import { eventIcon, eventSubjectLabel, eventSubjectLink } from '@/utils/eventSubject'

defineProps<{ events: RecentEvent[] | null; freshId: number | null }>()

const router = useRouter()

</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>Latest events</h2>
      <button class="btn btn-ghost btn-sm" @click="router.push('/apps')">View applications</button>
    </div>

    <div v-if="!events || events.length === 0" class="empty-state" style="padding: 28px">
      <span class="mdi mdi-timeline-text-outline" style="font-size: 32px; color: var(--text-muted)"></span>
      <p>No activity yet.</p>
    </div>

    <ul v-else class="timeline">
      <li
        v-for="e in events"
        :key="e.id"
        class="event row-clickable"
        :class="{ 'event-fresh': e.id === freshId }"
        @click="router.push(eventSubjectLink(e))"
      >
        <span class="event-icon" :class="`sev-${e.severity}`"><span class="mdi" :class="eventIcon(e.type)"></span></span>
        <div class="event-body">
          <div class="event-row">
            <span class="event-msg">{{ e.message || e.type }}</span>
            <span class="event-time">{{ relTime(e.created_at) }}</span>
          </div>
          <span class="event-type">{{ eventSubjectLabel(e) }} · {{ e.type }}</span>
        </div>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.timeline { list-style: none; margin: 0; padding: 8px 0; }
.event { display: flex; gap: 12px; padding: 10px 20px; }
.event + .event { border-top: 1px solid var(--border-secondary); }
/* Briefly highlight an event that just streamed in. */
.event-fresh { animation: event-fresh-in 2.4s ease-out; }
@keyframes event-fresh-in {
  0% { background: var(--primary-50); }
  100% { background: transparent; }
}
@media (prefers-reduced-motion: reduce) {
  .event-fresh { animation: none; }
}

.event-icon {
  flex-shrink: 0; width: 30px; height: 30px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center; font-size: 16px;
  background: var(--bg-tertiary); color: var(--text-secondary);
}
.event-icon.sev-warning { background: var(--warning-50); color: var(--warning-600); }
.event-icon.sev-error { background: var(--danger-50); color: var(--danger-600); }
.event-body { flex: 1; min-width: 0; }
.event-row { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.event-msg { font-size: 14px; color: var(--text-primary); }
.event-time { flex-shrink: 0; font-size: 12px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
.event-type { font-size: 11px; color: var(--text-muted); font-family: 'JetBrains Mono', monospace; }
</style>
