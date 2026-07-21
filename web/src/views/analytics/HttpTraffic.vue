<script setup lang="ts">
import AnalyticsShell from './AnalyticsShell.vue'
import Breakdown from './Breakdown.vue'
import WorldMap from '@/components/WorldMap.vue'
import type { AnalyticsReport } from '@/api/analytics'
import { fmtNum } from './format'

function statusTotal(r: AnalyticsReport): number {
  const s = r.status
  return Math.max(1, s.s2xx + s.s3xx + s.s4xx + s.s5xx)
}
</script>

<template>
  <AnalyticsShell v-slot="{ report }">
    <div class="card">
      <div class="a-card-header">
        <h3>Requests by country</h3>
        <span class="a-muted">{{ fmtNum(report.totals.requests) }} requests · {{ report.web.top_countries.length }} countries</span>
      </div>
      <div class="card-body">
        <WorldMap v-if="report.web.top_countries.length" :countries="report.web.top_countries" />
        <p v-else class="a-muted">No country data yet. Country enrichment needs the GeoIP database mounted on the gateway (GOMA_GEOIP_DB).</p>
      </div>
    </div>

    <div class="break-grid">
      <Breakdown title="Top countries" :items="report.web.top_countries" kind="country"
        empty-hint="Country data needs the GeoIP database on the gateway." />
      <Breakdown title="HTTP methods" :items="report.web.top_methods" />

      <div class="card">
        <div class="a-card-header"><h3>Status codes</h3></div>
        <div class="card-body">
          <div class="status-bar">
            <div class="seg seg-2xx" :style="{ width: (report.status.s2xx / statusTotal(report)) * 100 + '%' }"></div>
            <div class="seg seg-3xx" :style="{ width: (report.status.s3xx / statusTotal(report)) * 100 + '%' }"></div>
            <div class="seg seg-4xx" :style="{ width: (report.status.s4xx / statusTotal(report)) * 100 + '%' }"></div>
            <div class="seg seg-5xx" :style="{ width: (report.status.s5xx / statusTotal(report)) * 100 + '%' }"></div>
          </div>
          <div class="status-legend">
            <span><i class="dot dot-2xx"></i> 2xx <b>{{ fmtNum(report.status.s2xx) }}</b></span>
            <span><i class="dot dot-3xx"></i> 3xx <b>{{ fmtNum(report.status.s3xx) }}</b></span>
            <span><i class="dot dot-4xx"></i> 4xx <b>{{ fmtNum(report.status.s4xx) }}</b></span>
            <span><i class="dot dot-5xx"></i> 5xx <b>{{ fmtNum(report.status.s5xx) }}</b></span>
          </div>
        </div>
      </div>

      <Breakdown title="Top paths" :items="report.web.top_paths" />
      <Breakdown title="Referrers" :items="report.web.top_referrers" />
    </div>
  </AnalyticsShell>
</template>
