<script setup lang="ts">
import AnalyticsShell from './AnalyticsShell.vue'
import Breakdown from './Breakdown.vue'
import StatusPie from './StatusPie.vue'
import WorldMap from '@/components/WorldMap.vue'
import { ref } from 'vue'
import type { Category } from '@/api/analytics'
import { fmtNum } from './format'

const GEOIP_DOCS = 'https://docs.miabi.io/docs/operations/analytics#geoip-database'

// Hovering a list row lights up its country; a click (map or row) pins one.
const hovered = ref<string | null>(null)
const selected = ref<string | null>(null)
function toggle(alpha2: string) {
  selected.value = selected.value === alpha2 ? null : alpha2
}

// Every request carries a path, so the remainder is the long tail the server's top-K dropped.
function untracked(total: number, items: Category[]): number {
  return total - items.reduce((n, i) => n + i.count, 0)
}
</script>

<template>
  <AnalyticsShell v-slot="{ report }">
    <div class="card">
      <div class="a-card-header">
        <h3>{{ $t('analytics.requestsByCountry') }}</h3>
        <span class="a-muted">{{ fmtNum(report.totals.requests) }} requests · {{ report.web.top_countries.length }} countries</span>
      </div>
      <div class="card-body">
        <WorldMap
          v-if="report.web.top_countries.length"
          :countries="report.web.top_countries"
          :highlight="hovered ?? selected"
          @select="toggle"
        />
        <!-- Miabi ships no GeoIP database (licensing), so this empty state is the setup instructions. -->
        <div v-else class="empty-state">
          <h3>{{ $t('analytics.noCountryDataYet') }}</h3>
          <i18n-t keypath="analytics.geoipHint" tag="p">
            <template #path><code>/etc/miabi/geoip/country.mmdb</code></template>
          </i18n-t>
          <p>
            <a :href="GEOIP_DOCS" target="_blank" rel="noopener">{{ $t('analytics.whereToGetOne') }}</a>
          </p>
        </div>
      </div>
    </div>

    <div class="break-grid">
      <Breakdown :title="$t('analytics.countries')" :items="report.web.top_countries" kind="country" scroll share searchable linked
        :total="report.totals.requests" :active="selected"
        @hover="hovered = $event" @select="toggle"
        empty-hint="Needs a GeoIP database at /etc/miabi/geoip/country.mmdb — see the map above." />
      <Breakdown :title="$t('analytics.httpMethods')" :items="report.web.top_methods" />

      <div class="card">
        <div class="a-card-header"><h3>{{ $t('analytics.statusCodes') }}</h3></div>
        <div class="card-body">
          <StatusPie :status="report.status" />
        </div>
      </div>

      <Breakdown :title="$t('analytics.topPaths')" :items="report.web.top_paths" scroll share searchable
        :total="report.totals.requests" other-key="analytics.otherPaths"
        :untracked="untracked(report.totals.requests, report.web.top_paths)" />
      <Breakdown :title="$t('analytics.referrers')" :items="report.web.top_referrers" />
    </div>
  </AnalyticsShell>
</template>
