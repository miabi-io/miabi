<script setup lang="ts">
import AnalyticsShell from './AnalyticsShell.vue'
import Breakdown from './Breakdown.vue'
import StatusPie from './StatusPie.vue'
import WorldMap from '@/components/WorldMap.vue'
import { fmtNum } from './format'

const GEOIP_DOCS = 'https://docs.miabi.io/docs/operations/analytics#geoip-database'
</script>

<template>
  <AnalyticsShell v-slot="{ report }">
    <div class="card">
      <div class="a-card-header">
        <h3>{{ $t('analytics.requestsByCountry') }}</h3>
        <span class="a-muted">{{ fmtNum(report.totals.requests) }} requests · {{ report.web.top_countries.length }} countries</span>
      </div>
      <div class="card-body">
        <WorldMap v-if="report.web.top_countries.length" :countries="report.web.top_countries" />
        <!-- Miabi ships no GeoIP database (licensing — see docs), so for most installs this
             empty state IS the setup instructions. Keep it actionable: the path and the link
             are the whole point. -->
        <div v-else class="empty-state">
          <h3>{{ $t('analytics.noCountryDataYet') }}</h3>
          <i18n-t keypath="analytics.geoipHint" tag="p">
            <template #path><code>/etc/miabi/country.mmdb</code></template>
          </i18n-t>
          <p>
            <a :href="GEOIP_DOCS" target="_blank" rel="noopener">{{ $t('analytics.whereToGetOne') }}</a>
          </p>
        </div>
      </div>
    </div>

    <div class="break-grid">
      <Breakdown :title="$t('dashboard.analytics.topCountries')" :items="report.web.top_countries" kind="country"
        empty-hint="Needs a GeoIP database at /etc/miabi/country.mmdb — see the map above." />
      <Breakdown :title="$t('analytics.httpMethods')" :items="report.web.top_methods" />

      <div class="card">
        <div class="a-card-header"><h3>{{ $t('analytics.statusCodes') }}</h3></div>
        <div class="card-body">
          <StatusPie :status="report.status" />
        </div>
      </div>

      <Breakdown :title="$t('analytics.topPaths')" :items="report.web.top_paths" />
      <Breakdown :title="$t('analytics.referrers')" :items="report.web.top_referrers" />
    </div>
  </AnalyticsShell>
</template>
