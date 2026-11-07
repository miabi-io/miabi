<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useAnalyticsStore } from '@/stores/analytics'
import AnalyticsHeader from './AnalyticsHeader.vue'

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
        <h3>{{ $t('analytics.couldnTLoadAnalytics') }}</h3>
        <p>{{ error }}</p>
      </div>
    </div>

    <div v-else-if="report && report.totals.requests === 0" class="card">
      <div class="empty-state">
        <span class="mdi mdi-chart-box-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('analytics.noTrafficInThisWindow') }}</h3>
        <p>{{ $t('analytics.analyticsAreCollectedFromThe') }}</p>
      </div>
    </div>

    <div v-else-if="report" :class="{ 'a-refreshing': loading }">
      <slot :report="report" />
    </div>
  </div>
</template>

<style scoped>
.a-refreshing { opacity: 0.55; transition: opacity 0.15s; pointer-events: none; }
</style>
