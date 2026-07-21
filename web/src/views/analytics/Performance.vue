<script setup lang="ts">
import Sparkline from '@/components/Sparkline.vue'
import AnalyticsShell from './AnalyticsShell.vue'
import StatTile from './StatTile.vue'
import type { AnalyticsReport } from '@/api/analytics'
import { fmtNum, fmtMs, fmtPct, routeLabel } from './format'

function latency(r: AnalyticsReport): number[] {
  return r.series.map((p) => p.p95_latency_ms)
}
</script>

<template>
  <AnalyticsShell v-slot="{ report }">
    <div class="a-grid">
      <StatTile label="Avg latency" icon="mdi-clock-outline" :value="fmtMs(report.totals.avg_latency_ms)" />
      <StatTile label="p50" icon="mdi-speedometer-slow" :value="fmtMs(report.performance.request_p50_ms)" />
      <StatTile label="p95" icon="mdi-speedometer-medium" :value="fmtMs(report.performance.request_p95_ms)" />
      <StatTile label="p99" icon="mdi-speedometer" :value="fmtMs(report.performance.request_p99_ms)" />
    </div>

    <div class="two-col">
      <div class="card">
        <div class="a-card-header"><h3>p95 latency over time</h3><span class="a-muted">per {{ report.granularity }}</span></div>
        <div class="card-body">
          <Sparkline v-if="latency(report).length > 1" :values="latency(report)" :width="560" :height="90" stroke="var(--warning-600)" />
          <p v-else class="a-muted">Not enough data points to plot.</p>
        </div>
      </div>

      <div class="card">
        <div class="a-card-header"><h3>Where time goes</h3></div>
        <div class="card-body">
          <table class="mini-table">
            <thead><tr><th></th><th>p50</th><th>p95</th><th>p99</th></tr></thead>
            <tbody>
              <tr>
                <td>Total request</td>
                <td>{{ fmtMs(report.performance.request_p50_ms) }}</td>
                <td>{{ fmtMs(report.performance.request_p95_ms) }}</td>
                <td>{{ fmtMs(report.performance.request_p99_ms) }}</td>
              </tr>
              <tr>
                <td>Upstream (backend)</td>
                <td>{{ fmtMs(report.performance.upstream_p50_ms) }}</td>
                <td>{{ fmtMs(report.performance.upstream_p95_ms) }}</td>
                <td>{{ fmtMs(report.performance.upstream_p99_ms) }}</td>
              </tr>
            </tbody>
          </table>
          <div class="overhead">
            <span>Avg backend <b>{{ fmtMs(report.performance.avg_upstream_ms) }}</b></span>
            <span>Gateway overhead <b>{{ fmtMs(report.performance.avg_overhead_ms) }}</b></span>
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="a-card-header"><h3>Slowest routes</h3><span class="a-muted">by p95</span></div>
      <div class="table-wrapper">
        <table>
          <thead><tr><th>Route</th><th class="text-right">Requests</th><th class="text-right">p95</th><th class="text-right">Errors</th></tr></thead>
          <tbody>
            <tr v-for="r in report.performance.slow_routes" :key="r.route">
              <td class="cell-title">{{ routeLabel(r.route) }}</td>
              <td class="text-right a-muted">{{ fmtNum(r.requests) }}</td>
              <td class="text-right">{{ fmtMs(r.p95_latency_ms) }}</td>
              <td class="text-right" :class="{ 'a-danger': r.error_rate >= 0.05 }">{{ fmtPct(r.error_rate) }}</td>
            </tr>
            <tr v-if="!report.performance.slow_routes.length"><td colspan="4" class="a-muted">No route data.</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </AnalyticsShell>
</template>

<style scoped>
.two-col { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 14px; }
.overhead { display: flex; gap: 20px; margin-top: 14px; font-size: 13px; color: var(--text-secondary); }
.overhead b { color: var(--text-primary); }
</style>
