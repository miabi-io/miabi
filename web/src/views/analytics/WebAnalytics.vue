<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import AnalyticsShell from './AnalyticsShell.vue'
import StatTile from './StatTile.vue'
import Breakdown from './Breakdown.vue'
import type { AnalyticsReport } from '@/api/analytics'
import { fmtNum, delta } from './format'

const ws = useWorkspaceStore()
const { contextLabel } = storeToRefs(ws)

function perVisitor(r: AnalyticsReport): string {
  return r.web.unique_visitors ? (r.totals.requests / r.web.unique_visitors).toFixed(1) : '—'
}
function botPct(r: AnalyticsReport): number {
  const t = r.web.bot_requests + r.web.human_requests
  return t ? (r.web.bot_requests / t) * 100 : 0
}
</script>

<template>
  <AnalyticsShell v-slot="{ report }">
    <h2 class="web-title">Web Analytics for {{ contextLabel }}</h2>

    <div class="a-grid">
      <StatTile label="Unique visitors" icon="mdi-account-multiple-outline" :value="fmtNum(report.web.unique_visitors)"
        sub="cookieless · daily-salted"
        :delta="delta(report.web.unique_visitors, report.compare?.unique_visitors)" />
      <StatTile label="Page views" icon="mdi-eye-outline" :value="fmtNum(report.totals.requests)"
        :delta="delta(report.totals.requests, report.compare?.requests)" />
      <StatTile label="Views / visitor" icon="mdi-account-eye-outline" :value="perVisitor(report)" />
      <StatTile label="Human traffic" icon="mdi-account-check-outline" :value="fmtNum(report.web.human_requests)"
        :sub="`${fmtNum(report.web.bot_requests)} bot requests`" />
    </div>

    <div class="card">
      <div class="a-card-header"><h3>Human vs bot</h3><span class="a-muted">{{ botPct(report).toFixed(1) }}% automated</span></div>
      <div class="card-body">
        <div class="status-bar">
          <div class="seg seg-2xx" :style="{ width: (100 - botPct(report)) + '%' }"></div>
          <div class="seg seg-4xx" :style="{ width: botPct(report) + '%' }"></div>
        </div>
        <div class="status-legend">
          <span><i class="dot dot-2xx"></i> Human <b>{{ fmtNum(report.web.human_requests) }}</b></span>
          <span><i class="dot dot-4xx"></i> Bot <b>{{ fmtNum(report.web.bot_requests) }}</b></span>
        </div>
      </div>
    </div>

    <div class="break-grid">
      <Breakdown title="Top pages" :items="report.web.top_paths" />
      <Breakdown title="Referrers" :items="report.web.top_referrers" />
      <Breakdown title="Countries" :items="report.web.top_countries" kind="country"
        empty-hint="Country data needs the GeoIP database on the gateway." />
      <Breakdown title="Browsers" :items="report.web.top_browsers" />
      <Breakdown title="Operating systems" :items="report.web.top_os" />
      <Breakdown title="Devices" :items="report.web.top_devices" />
    </div>
  </AnalyticsShell>
</template>

<style scoped>
.web-title { margin: 0 0 14px; font-size: 18px; color: var(--text-primary); }
</style>
