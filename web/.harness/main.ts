import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory, RouterView } from 'vue-router'
import { i18n } from '@/i18n'
import '@mdi/font/css/materialdesignicons.css'
import '@/assets/styles.css'
import '@/assets/accents.css'
import HttpTraffic from '@/views/analytics/HttpTraffic.vue'
import { useAnalyticsStore } from '@/stores/analytics'
import api from '@/api/client'

api.interceptors.request.clear()
api.interceptors.response.clear()
api.defaults.adapter = async (config) => ({ data: { success: true, data: null }, status: 200, statusText: 'OK', headers: {}, config })

const params = new URLSearchParams(location.search)
if (params.get('theme') === 'dark') document.documentElement.setAttribute('data-theme', 'dark')

const CODES = ('US DE FR GB NL CA JP IN BR CN AU ES IT SE PL CH BE AT NO DK FI IE PT CZ RO HU GR TR RU UA ' +
  'IL AE SA ZA NG KE EG MA MX AR CL CO PE KR TW SG HK MY TH VN ID PH NZ PK BD LK NP IR IQ JO LB ' +
  'CD CM CI SN GH ET TZ UG RW AO ZM ZW BW NA MZ MG TN DZ LY SD EE LV LT SK SI HR RS BG IS LU MT CY').split(' ')
const top_countries = CODES.map((label, i) => ({ label, count: Math.max(1, Math.round(48000 / Math.pow(i + 1, 1.15))) }))
const requests = Math.round(top_countries.reduce((n, c) => n + c.count, 0) * 1.04)
const cat = (labels: string[], top: number) => labels.map((label, i) => ({ label, count: Math.round(top / (i + 1)) }))
const now = Date.now()
const series = Array.from({ length: 24 }, (_, i) => ({
  t: new Date(now - (23 - i) * 3600e3).toISOString(), requests: 4000 + Math.round(Math.sin(i / 3) * 1500),
  bytes_in: 0, bytes_out: 0, errors: 40, errors_4xx: 30, errors_5xx: 10, unique_visitors: 900, avg_latency_ms: 42, p95_latency_ms: 180,
}))

const report = {
  range: { since: new Date(now - 24 * 3600e3).toISOString(), until: new Date(now).toISOString() },
  granularity: 'hour', series, retention_days: -1, exportable: true,
  totals: { requests, bytes_in: 1.2e9, bytes_out: 8.4e9, unique_visitors: 21450, error_rate: 0.021, avg_latency_ms: 42, p50_latency_ms: 28, p95_latency_ms: 180, p99_latency_ms: 420 },
  status: { s2xx: Math.round(requests * 0.9), s3xx: Math.round(requests * 0.06), s4xx: Math.round(requests * 0.035), s5xx: Math.round(requests * 0.005) },
  performance: { request_p50_ms: 28, request_p95_ms: 180, request_p99_ms: 420, upstream_p50_ms: 22, upstream_p95_ms: 160, upstream_p99_ms: 390, avg_request_ms: 42, avg_upstream_ms: 36, avg_overhead_ms: 6, slow_routes: [] },
  web: {
    unique_visitors: 21450, bot_requests: 8000, human_requests: requests - 8000, top_countries,
    top_paths: cat(['/', '/api/health', '/login', '/dashboard', '/static/app.js', '/static/app.css', '/favicon.ico', '/api/v1/items', '/robots.txt', '/wp-login.php'], 52000),
    top_referrers: cat(['', 'google.com', 'github.com', 'news.ycombinator.com', 'reddit.com', 'twitter.com'], 30000),
    top_methods: cat(['GET', 'POST', 'PUT', 'DELETE', 'OPTIONS', 'HEAD'], 140000),
    top_browsers: [], top_user_agents: [], top_os: [], top_devices: [],
  },
}

const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/analytics/http', component: HttpTraffic }, { path: '/:p(.*)*', component: HttpTraffic }] })
const app = createApp({ render: () => h(RouterView) })
app.use(createPinia()).use(i18n).use(router)
router.push('/analytics/http')
router.isReady().then(() => {
  app.mount('#app')
  const store = useAnalyticsStore()
  setTimeout(() => {
    store.report = report as never
    store.loading = false
    const q = params.get('q')
    if (q) setTimeout(() => {
      const input = document.querySelector<HTMLInputElement>('.bd-search input')
      if (input) { input.value = q; input.dispatchEvent(new Event('input')) }
    }, 200)
    if (params.get('custom')) setTimeout(() => {
      const btns = [...document.querySelectorAll<HTMLButtonElement>('.a-range-btn')]
      btns[btns.length - 1]?.click()
    }, 200)
    const scroll = params.get('scroll')
    if (scroll) setTimeout(() => { const el = document.querySelector('.bd-scroll'); if (el) el.scrollTop = Number(scroll) }, 200)
  }, 300)
})
