<script setup lang="ts">
import AnalyticsShell from './AnalyticsShell.vue'
import StatTile from './StatTile.vue'
import LatencyChart from './LatencyChart.vue'
import { fmtNum, fmtMs, fmtPct, routeLabel } from './format'
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
        <div class="a-card-header"><h3>{{ $t('analytics.p95LatencyOverTime') }}</h3><span class="a-muted">per {{ report.granularity }}</span></div>
        <div class="card-body">
          <LatencyChart v-if="report.series.length" :series="report.series" :granularity="report.granularity" :height="150" />
          <p v-else class="a-muted">{{ $t('analytics.notEnoughDataPointsTo') }}</p>
        </div>
      </div>

      <div class="card">
        <div class="a-card-header"><h3>{{ $t('analytics.whereTimeGoes') }}</h3></div>
        <div class="card-body">
          <table class="mini-table">
            <thead><tr><th></th><th>p50</th><th>p95</th><th>p99</th></tr></thead>
            <tbody>
              <tr>
                <td>{{ $t('analytics.totalRequest') }}</td>
                <td>{{ fmtMs(report.performance.request_p50_ms) }}</td>
                <td>{{ fmtMs(report.performance.request_p95_ms) }}</td>
                <td>{{ fmtMs(report.performance.request_p99_ms) }}</td>
              </tr>
              <tr>
                <td>{{ $t('analytics.upstreamBackend') }}</td>
                <td>{{ fmtMs(report.performance.upstream_p50_ms) }}</td>
                <td>{{ fmtMs(report.performance.upstream_p95_ms) }}</td>
                <td>{{ fmtMs(report.performance.upstream_p99_ms) }}</td>
              </tr>
            </tbody>
          </table>
          <div class="overhead">
            <span>{{ $t('analytics.avgBackend') }} <b>{{ fmtMs(report.performance.avg_upstream_ms) }}</b></span>
            <span>{{ $t('analytics.gatewayOverhead') }} <b>{{ fmtMs(report.performance.avg_overhead_ms) }}</b></span>
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="a-card-header"><h3>{{ $t('analytics.slowestRoutes') }}</h3><span class="a-muted">{{ $t('analytics.byP95') }}</span></div>
      <div class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('analytics.route') }}</th><th class="text-right">{{ $t('analytics.requests') }}</th><th class="text-right">p95</th><th class="text-right">{{ $t('analytics.errors') }}</th></tr></thead>
          <tbody>
            <tr v-for="r in report.performance.slow_routes" :key="r.route">
              <td class="cell-title">{{ routeLabel(r.route) }}</td>
              <td class="text-right a-muted">{{ fmtNum(r.requests) }}</td>
              <td class="text-right">{{ fmtMs(r.p95_latency_ms) }}</td>
              <td class="text-right" :class="{ 'a-danger': r.error_rate >= 0.05 }">{{ fmtPct(r.error_rate) }}</td>
            </tr>
            <tr v-if="!report.performance.slow_routes.length"><td colspan="4" class="a-muted">{{ $t('analytics.noRouteData') }}</td></tr>
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
