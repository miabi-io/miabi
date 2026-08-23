<script setup lang="ts">
// Shortcuts to the things people come here to start. Everything here is also in
// the sidebar, so these stay compact chips rather than cards.
//
// One flat list, ordered so related actions sit next to each other (create →
// expose → configure → automate) and the accent colour follows that run. The
// list is deliberately short: quick actions are for what you *start* often, not
// everything you can manage — the rest is a sidebar click away.
import { useRouter } from 'vue-router'

const router = useRouter()

const actions = [
  { to: '/apps', icon: 'mdi-cube-outline', label: 'Deploy application', hint: 'From an image or a Git repository', tone: 'primary' },
  { to: '/databases', icon: 'mdi-database-plus-outline', label: 'New database', hint: 'Postgres, MySQL, Redis…', tone: 'primary' },
  { to: '/stacks', icon: 'mdi-layers-outline', label: 'Create stack', hint: 'Compose multiple applications', tone: 'primary' },
  { to: '/marketplace', icon: 'mdi-storefront-outline', label: 'Marketplace', hint: 'One-click apps and databases', tone: 'primary' },
  { to: '/routes', icon: 'mdi-routes', label: 'Add route', hint: 'Expose an application on a domain', tone: 'success' },
  { to: '/domains', icon: 'mdi-web', label: 'Add domain', hint: 'Add and verify a domain you own', tone: 'success' },
  { to: '/secrets', icon: 'mdi-key-variant', label: 'Add secret', hint: 'Store a reusable value', tone: 'info' },
  { to: '/gitops', icon: 'mdi-git', label: 'GitOps', hint: 'Deploy from a Git repository on push', tone: 'warning' },
  { to: '/pipelines', icon: 'mdi-pipe', label: 'Pipeline', hint: 'Build and deploy in stages', tone: 'warning' },
  { to: '/jobs', icon: 'mdi-clock-outline', label: 'Scheduled job', hint: 'Run a container on a schedule', tone: 'warning' },
]
</script>

<template>
  <div class="quick-actions">
    <button
      v-for="a in actions"
      :key="a.to"
      class="qa"
      :class="`qa-${a.tone}`"
      :title="a.hint"
      @click="router.push(a.to)"
    >
      <span class="qa-icon"><span class="mdi" :class="a.icon"></span></span>
      <span class="qa-label">{{ a.label }}</span>
    </button>
  </div>
</template>

<style scoped>
.quick-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 20px; }

.qa {
  display: inline-flex; align-items: center; gap: 7px;
  padding: 7px 13px 7px 10px; border-radius: var(--radius);
  background: var(--bg-primary); border: 1px solid var(--border-primary);
  color: var(--text-primary); font-size: 13px; font-weight: 500;
  cursor: pointer; transition: border-color 0.15s, background 0.15s, transform 0.15s;
}
.qa:hover { border-color: var(--qa-color); background: var(--bg-hover); transform: translateY(-1px); }
.qa-icon { display: inline-flex; font-size: 16px; color: var(--qa-color); }

.qa-primary { --qa-color: var(--primary-500); }
.qa-info { --qa-color: var(--info-500, #0ea5e9); }
.qa-success { --qa-color: var(--success-600); }
.qa-warning { --qa-color: var(--warning-600); }

@media (max-width: 719px) {
  /* One scrollable row beats a dozen chips wrapping the dashboard off-screen. */
  .quick-actions {
    flex-wrap: nowrap; overflow-x: auto; scrollbar-width: none;
    -webkit-overflow-scrolling: touch; padding-bottom: 2px;
  }
  .quick-actions::-webkit-scrollbar { display: none; }
  .qa { flex: 0 0 auto; }
}
</style>
