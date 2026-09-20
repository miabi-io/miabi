<script setup lang="ts">
import { useRouter } from 'vue-router'
import type { AppHealth } from '@/api/types'
import { fmtDate, healthBadge, statusBadge } from './format'

defineProps<{ apps: AppHealth[] | null; canEdit: boolean }>()

const router = useRouter()
</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>{{ $t('nav.deploy.applications') }}</h2>
      <button class="btn btn-ghost btn-sm" @click="router.push('/apps')">{{ $t('dashboard.viewAll') }}</button>
    </div>

    <div v-if="!apps || apps.length === 0" class="empty-state">
      <span class="mdi mdi-cube-outline" style="font-size: 40px; color: var(--text-muted)"></span>
      <h3>{{ $t('dashboard.apps.empty') }}</h3>
      <p>{{ $t('dashboard.apps.emptyHint') }}</p>
      <button v-if="canEdit" class="btn btn-primary mt-4" @click="router.push('/apps')">{{ $t('dashboard.action.deployApplication') }}</button>
    </div>

    <div v-else class="table-wrapper">
      <table>
        <thead>
          <tr><th>{{ $t('dashboard.col.application') }}</th><th>{{ $t('dashboard.col.node') }}</th><th>{{ $t('dashboard.col.created') }}</th><th>{{ $t('dashboard.col.status') }}</th><th class="text-right">{{ $t('dashboard.col.health') }}</th></tr>
        </thead>
        <tbody>
          <tr v-for="a in apps" :key="a.id" class="row-clickable" @click="router.push(`/apps/${a.id}`)">
            <td>
              <div class="cell-id">
                <span class="avatar avatar-sm">{{ (a.display_name || a.name).charAt(0).toUpperCase() }}</span>
                <span class="cell-text">
                  <span class="cell-title">{{ a.display_name || a.name }}</span>
                  <span class="cell-sub">{{ a.name }}</span>
                </span>
              </div>
            </td>
            <td>
              <span class="node-cell"><span class="mdi mdi-server-network"></span> {{ a.server_name || 'Local' }}</span>
            </td>
            <td class="cell-sub">{{ fmtDate(a.created_at) }}</td>
            <td><span class="badge badge-dot" :class="statusBadge(a.status)">{{ a.status }}</span></td>
            <td class="text-right"><span class="badge" :class="healthBadge(a.health)">{{ a.health }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.node-cell { display: inline-flex; align-items: center; gap: 5px; font-size: 13px; color: var(--text-secondary); }
.node-cell .mdi { font-size: 15px; color: var(--text-muted); }
</style>
