<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { infoApi } from '@/api/info'
import { copyText } from '@/utils/clipboard'
import type { AppInfo } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useLicenseStore } from '@/stores/license'

const auth = useAuthStore()
const license = useLicenseStore()

const info = ref<AppInfo | null>(null)
const copied = ref(false)

const docsUrl = ((import.meta.env.VITE_API_URL as string) || '/api/v1').replace(/\/api\/v1\/?$/, '') + '/docs'
const currentYear = new Date().getFullYear()

// Edition badge: only meaningful once the license store has loaded (admins).
const edition = computed(() => (license.loaded ? license.edition : null))
const editionLabel = computed(() => {
  switch (edition.value) {
    case 'enterprise':
      return 'Enterprise'
    case 'community':
      return 'Community'
    default:
      return null
  }
})

const resources = [
  { label: 'Documentation', icon: 'mdi-book-open-page-variant-outline', href: 'https://docs.miabi.io' },
  { label: 'Source code', icon: 'mdi-github', href: 'https://github.com/miabi-io/miabi' },
  { label: 'Report an issue', icon: 'mdi-bug-outline', href: 'https://github.com/miabi-io/miabi/issues' },
]

const ecosystem = [
  { name: 'Goma Gateway', desc: 'Reverse proxy & TLS termination', href: 'https://github.com/jkaninda/goma-gateway', icon: 'mdi-transit-connection-variant' },
  { name: 'Posta', desc: 'Self-hosted email platform', href: 'https://github.com/goposta/posta', icon: 'mdi-email-outline' },
  { name: 'pg-bkup', desc: 'PostgreSQL backup tool', href: 'https://github.com/jkaninda/pg-bkup', icon: 'mdi-database-outline' },
  { name: 'mysql-bkup', desc: 'MySQL/MariaDB backup tool', href: 'https://github.com/jkaninda/mysql-bkup', icon: 'mdi-database-outline' },
  { name: 'mongodb-bkup', desc: 'MongoDB backup tool', href: 'https://github.com/jkaninda/mongodb-bkup', icon: 'mdi-leaf' },
  { name: 'volume-bkup', desc: 'Docker volume backup tool', href: 'https://github.com/jkaninda/volume-bkup', icon: 'mdi-harddisk' },
]

async function load() {
  try {
    info.value = (await infoApi.get()).data.data
  } catch {
    info.value = null
  }
  if (auth.isAdmin) license.load().catch(() => {})
}
onMounted(load)

function copyVersion() {
  const v = `${info.value?.name ?? 'Miabi'} ${info.value?.version ?? ''} (${info.value?.commit_id ?? ''})`.trim()
  void copyText(v).then((ok) => {
    if (!ok) return
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  })
}
</script>

<template>
  <div class="about">
    <!-- Hero -->
    <section class="hero">
      <img src="/brand/miabi-mark.svg" alt="Miabi" class="hero-mark" />
      <h1 class="hero-title">Miabi<span class="hero-accent">.io</span></h1>
      <p class="hero-tagline">The open-source, self-hosted Platform-as-a-Service (PaaS) for Docker.</p>
      <div class="hero-badges">
        <span v-if="info" class="badge badge-neutral">v{{ info.version }}</span>
        <span v-if="editionLabel" class="badge edition" :class="`edition-${edition}`">{{ editionLabel }} Edition</span>
      </div>
    </section>

    <div class="grid">
      <!-- Build info -->
      <div class="card">
        <div class="card-header"><h2>Build</h2></div>
        <div class="card-body">
          <div class="kv"><span class="k">Version</span><span class="v">{{ info?.version ?? '—' }}</span></div>
          <div class="kv"><span class="k">Commit</span><span class="v mono">{{ info?.commit_id ?? '—' }}</span></div>
          <div class="kv">
            <span class="k">Edition</span>
            <span class="v">{{ editionLabel ?? (auth.isAdmin ? '—' : 'Restricted') }}</span>
          </div>
          <button class="btn btn-secondary btn-sm copy-btn" @click="copyVersion">
            <span class="mdi" :class="copied ? 'mdi-check' : 'mdi-content-copy'"></span>
            {{ copied ? 'Copied' : 'Copy version' }}
          </button>
        </div>
      </div>

      <!-- About -->
      <div class="card">
        <div class="card-header"><h2>About</h2></div>
        <div class="card-body">
          <p class="prose">
            Miabi lets you deploy apps, manage domains, provision databases, get automatic SSL, monitor
            services, and manage teams from one web interface — without touching Docker commands.
          </p>
          <p class="prose">
            It's <strong>API-first</strong>, <strong>Docker-first</strong>, multi-tenant, and
            self-hosted first. AGPL-3.0 licensed and open-source friendly.
          </p>
        </div>
      </div>

      <!-- Resources -->
      <div class="card">
        <div class="card-header"><h2>Resources</h2></div>
        <div class="card-body link-list">
          <a v-if="info?.openapi_docs" :href="docsUrl" target="_blank" rel="noopener noreferrer" class="link-row">
            <span class="mdi mdi-api"></span> API Reference <span class="mdi mdi-open-in-new ext"></span>
          </a>
          <a v-for="r in resources" :key="r.label" :href="r.href" target="_blank" rel="noopener noreferrer" class="link-row">
            <span class="mdi" :class="r.icon"></span> {{ r.label }} <span class="mdi mdi-open-in-new ext"></span>
          </a>
          <router-link v-if="auth.isAdmin" to="/admin/license" class="link-row">
            <span class="mdi mdi-license"></span> License &amp; entitlements
          </router-link>
        </div>
      </div>

      <!-- Ecosystem -->
      <div class="card ecosystem-card">
        <div class="card-header"><h2>Ecosystem</h2></div>
        <div class="card-body ecosystem">
          <a v-for="e in ecosystem" :key="e.name" :href="e.href" target="_blank" rel="noopener noreferrer" class="eco-item">
            <span class="mdi eco-icon" :class="e.icon"></span>
            <span class="eco-text">
              <span class="eco-name">{{ e.name }} <span class="mdi mdi-open-in-new ext"></span></span>
              <span class="eco-desc">{{ e.desc }}</span>
            </span>
          </a>
        </div>
      </div>
    </div>

    <p class="legal">&copy; {{ currentYear }} Jonas Kaninda · AGPL-3.0 licensed</p>
  </div>
</template>

<style scoped>
.about {
  max-width: 920px;
  margin: 0 auto;
}
.hero {
  text-align: center;
  padding: 28px 0 24px;
}
.hero-mark {
  width: 64px;
  height: 64px;
  margin-bottom: 14px;
}
.hero-title {
  font-size: 34px;
  font-weight: 700;
  margin: 0;
  letter-spacing: -0.02em;
}
.hero-accent {
  color: var(--accent, #6366f1);
}
.hero-tagline {
  margin: 8px 0 16px;
  color: var(--text-secondary, var(--text-muted));
  font-size: 15px;
}
.hero-badges {
  display: flex;
  gap: 8px;
  justify-content: center;
}
.badge.edition {
  font-weight: 600;
}
.edition-enterprise {
  background: var(--accent-bg, rgba(99, 102, 241, 0.12));
  color: var(--accent, #6366f1);
}
.edition-community {
  background: var(--bg-tertiary, rgba(127, 127, 127, 0.14));
  color: var(--text-secondary, var(--text-muted));
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}
.card-header h2 {
  font-size: 13px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-secondary, var(--text-muted));
}
.kv {
  display: flex;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--border-primary);
  font-size: 14px;
}
.kv .k {
  color: var(--text-muted);
}
.kv .v {
  font-weight: 500;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.copy-btn {
  margin-top: 14px;
}
.prose {
  margin: 0 0 12px;
  line-height: 1.6;
  font-size: 14px;
  color: var(--text-secondary, var(--text-muted));
}
.prose:last-child {
  margin-bottom: 0;
}
.prose strong {
  color: var(--text-primary);
}
.link-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.link-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 8px;
  border-radius: 8px;
  text-decoration: none;
  color: var(--text-primary);
  font-size: 14px;
}
.link-row:hover {
  background: var(--bg-tertiary, rgba(127, 127, 127, 0.08));
}
.link-row .ext {
  margin-left: auto;
  font-size: 13px;
  color: var(--text-muted);
}
.ecosystem-card {
  grid-column: 1 / -1;
}
.ecosystem {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px;
}
.eco-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  border-radius: 10px;
  border: 1px solid var(--border-primary);
  text-decoration: none;
  color: var(--text-primary);
}
.eco-item:hover {
  border-color: var(--accent, #6366f1);
}
.eco-icon {
  font-size: 22px;
  color: var(--accent, #6366f1);
}
.eco-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.eco-name {
  font-weight: 600;
  font-size: 14px;
}
.eco-name .ext {
  font-size: 12px;
  color: var(--text-muted);
}
.eco-desc {
  font-size: 12px;
  color: var(--text-muted);
}
.legal {
  text-align: center;
  margin: 28px 0 8px;
  font-size: 12px;
  color: var(--text-muted);
}
@media (max-width: 720px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
