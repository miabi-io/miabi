<script setup lang="ts">
// Shortcuts to the things people come here to start. Everything here is also in
// the sidebar, so these stay compact chips rather than cards.
//
// One flat list, ordered so related actions sit next to each other (create →
// expose → configure → automate) and the accent colour follows that run. The
// list is deliberately short: quick actions are for what you *start* often, not
// everything you can manage — the rest is a sidebar click away.
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

const router = useRouter()
const { t } = useI18n()

const actions = computed(() => [
  { to: '/apps', icon: 'mdi-cube-outline', label: t('dashboard.quick.deployApplication.label'), hint: t('dashboard.quick.deployApplication.hint'), tone: 'primary' },
  { to: '/databases', icon: 'mdi-database-plus-outline', label: t('dashboard.quick.newDatabase.label'), hint: t('dashboard.quick.newDatabase.hint'), tone: 'primary' },
  { to: '/stacks', icon: 'mdi-layers-outline', label: t('dashboard.quick.createStack.label'), hint: t('dashboard.quick.createStack.hint'), tone: 'primary' },
  { to: '/marketplace', icon: 'mdi-storefront-outline', label: t('dashboard.quick.marketplace.label'), hint: t('dashboard.quick.marketplace.hint'), tone: 'primary' },
  { to: '/routes', icon: 'mdi-routes', label: t('dashboard.quick.addRoute.label'), hint: t('dashboard.quick.addRoute.hint'), tone: 'success' },
  { to: '/domains', icon: 'mdi-web', label: t('dashboard.quick.addDomain.label'), hint: t('dashboard.quick.addDomain.hint'), tone: 'success' },
  { to: '/secrets', icon: 'mdi-key-variant', label: t('dashboard.quick.addSecret.label'), hint: t('dashboard.quick.addSecret.hint'), tone: 'info' },
  { to: '/gitops', icon: 'mdi-git', label: t('dashboard.quick.gitops.label'), hint: t('dashboard.quick.gitops.hint'), tone: 'warning' },
  { to: '/pipelines', icon: 'mdi-pipe', label: t('dashboard.quick.pipeline.label'), hint: t('dashboard.quick.pipeline.hint'), tone: 'warning' },
  { to: '/jobs', icon: 'mdi-clock-outline', label: t('dashboard.quick.scheduledJob.label'), hint: t('dashboard.quick.scheduledJob.hint'), tone: 'warning' },
])
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
