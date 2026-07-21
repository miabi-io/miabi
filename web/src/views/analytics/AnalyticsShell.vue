<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useAnalyticsStore } from '@/stores/analytics'
import AnalyticsHeader from './AnalyticsHeader.vue'

// Wraps every analytics page: renders the shared header and the loading/empty/
// error states, exposing the loaded report to the page via the default slot.
const store = useAnalyticsStore()
const { report, loading, error } = storeToRefs(store)
</script>

<template>
  <div>
    <AnalyticsHeader />

    <div v-if="loading && !report" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <div v-else-if="error" class="card">
      <div class="empty-state">
        <span class="mdi mdi-alert-circle-outline" style="font-size: 40px; color: var(--danger-500)"></span>
        <h3>Couldn't load analytics</h3>
        <p>{{ error }}</p>
      </div>
    </div>

    <div v-else-if="report && report.totals.requests === 0" class="card">
      <div class="empty-state">
        <span class="mdi mdi-chart-box-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No traffic in this window</h3>
        <p>Analytics are collected from the gateway as requests reach your routed apps. Try a wider range.</p>
      </div>
    </div>

    <slot v-else-if="report" :report="report" />
  </div>
</template>
