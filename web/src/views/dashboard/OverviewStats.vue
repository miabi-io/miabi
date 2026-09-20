<script setup lang="ts">
import { useRouter } from 'vue-router'
import type { Overview } from '@/api/types'

defineProps<{ overview: Overview }>()

const router = useRouter()
</script>

<template>
  <div class="stats-grid">
    <div class="stat-card stat-card-clickable" @click="router.push('/apps')">
      <div class="stat-header">
        <span class="stat-label">{{ $t('nav.deploy.applications') }}</span>
        <span class="stat-icon stat-icon-primary"><span class="mdi mdi-cube-outline"></span></span>
      </div>
      <div class="stat-value">{{ overview.total_apps }}</div>
    </div>
    <div class="stat-card stat-card-clickable" @click="router.push('/apps')">
      <div class="stat-header">
        <span class="stat-label">{{ $t('dashboard.stats.running') }}</span>
        <span class="stat-icon stat-icon-success"><span class="mdi mdi-play-circle-outline"></span></span>
      </div>
      <div class="stat-value">{{ overview.running }}</div>
    </div>
    <div
      class="stat-card stat-card-clickable"
      :class="{ 'stat-card-alert': overview.failed > 0 }"
      @click="router.push('/apps')"
    >
      <div class="stat-header">
        <span class="stat-label">{{ $t('dashboard.stats.failed') }}</span>
        <span class="stat-icon stat-icon-danger"><span class="mdi mdi-alert-circle-outline"></span></span>
      </div>
      <div class="stat-value">{{ overview.failed }}</div>
    </div>
    <div class="stat-card stat-card-clickable" @click="router.push('/databases')">
      <div class="stat-header">
        <span class="stat-label">{{ $t('nav.data.databases') }}</span>
        <span class="stat-icon stat-icon-info"><span class="mdi mdi-database-outline"></span></span>
      </div>
      <div class="stat-value">{{ overview.databases }}</div>
    </div>
    <div class="stat-card stat-card-clickable" @click="router.push('/stacks')">
      <div class="stat-header">
        <span class="stat-label">{{ $t('nav.deploy.stacks') }}</span>
        <span class="stat-icon stat-icon-primary"><span class="mdi mdi-layers-outline"></span></span>
      </div>
      <div class="stat-value">{{ overview.stacks }}</div>
    </div>
  </div>
</template>

<style scoped>
.stat-card-clickable { cursor: pointer; transition: border-color 0.15s, transform 0.15s; }
.stat-card-clickable:hover { border-color: var(--border-primary); transform: translateY(-1px); }
.stat-card-alert { border-color: var(--danger-500); }
</style>
