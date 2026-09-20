<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { memberApi } from '@/api/resources'
import type { AuditLogDetail } from '@/api/types'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const route = useRoute()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const entry = ref<AuditLogDetail | null>(null)
const loading = ref(false)

const entryId = computed(() => Number(route.params.id))

async function load() {
  const wsId = currentWorkspaceId.value
  if (!wsId || !entryId.value) return
  loading.value = true
  try {
    entry.value = (await memberApi.auditLog(wsId, entryId.value)).data.data
  } catch (e) {
    notify.apiError(e, 'Failed to load audit entry')
    router.replace('/audit-log')
  } finally {
    loading.value = false
  }
}

watch([currentWorkspaceId, entryId], load, { immediate: true })

function actionBadge(action: string): string {
  if (action.includes('delete') || action.includes('remove')) return 'badge-danger'
  if (action.includes('create') || action.includes('invite')) return 'badge-success'
  if (action.includes('update') || action.includes('role') || action.includes('set')) return 'badge-warning'
  return 'badge-neutral'
}

function when(ts: string): string {
  return new Date(ts).toLocaleString()
}

const actorLabel = computed(() => {
  const e = entry.value
  if (!e) return '—'
  if (e.actor_name || e.actor_email) {
    return e.actor_name && e.actor_email ? `${e.actor_name} · ${e.actor_email}` : e.actor_name || e.actor_email || ''
  }
  return e.actor_id ? `#${e.actor_id}` : 'System'
})

// Pretty-print the metadata object; empty when there's none.
const prettyMetadata = computed(() => {
  const m = entry.value?.metadata
  if (!m || Object.keys(m).length === 0) return ''
  return JSON.stringify(m, null, 2)
})
</script>

<template>
  <div>
    <div class="page-header">
      <div class="header-left">
        <button class="btn-icon btn-icon-muted" :title="$t('audit.backToAuditLog')" :aria-label="$t('audit.backToAuditLog')" @click="router.push('/audit-log')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div class="header-title">
          <h1>{{ $t('audit.auditEntry') }}</h1>
          <span class="subline">Activity in {{ ws.contextLabel }}</span>
        </div>
      </div>
    </div>

    <div v-if="loading && !entry" class="card">
      <div class="card-body"><span class="spinner"></span></div>
    </div>

    <template v-else-if="entry">
      <div class="card">
        <div class="card-header">
          <h2>
            <span class="badge" :class="actionBadge(entry.action)">{{ entry.action }}</span>
          </h2>
        </div>
        <div class="card-body details">
          <div class="detail">
            <span class="text-muted">{{ $t('audit.entryId') }}</span>
            <span><code>{{ entry.id }}</code></span>
          </div>
          <div class="detail">
            <span class="text-muted">{{ $t('audit.action') }}</span>
            <span><code>{{ entry.action }}</code></span>
          </div>
          <div class="detail">
            <span class="text-muted">{{ $t('audit.actor') }}</span>
            <span>{{ actorLabel }}</span>
          </div>
          <div class="detail">
            <span class="text-muted">{{ $t('audit.target') }}</span>
            <span>{{ entry.target_type || '—' }}<template v-if="entry.target_id"> #{{ entry.target_id }}</template></span>
          </div>
          <div class="detail">
            <span class="text-muted">{{ $t('appDetail.ipAddress') }}</span>
            <span><code v-if="entry.ip_address">{{ entry.ip_address }}</code><span v-else>—</span></span>
          </div>
          <div class="detail">
            <span class="text-muted">{{ $t('audit.when') }}</span>
            <span>{{ when(entry.created_at) }}</span>
          </div>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header"><h2>{{ $t('audit.metadata') }}</h2></div>
        <div v-if="!prettyMetadata" class="empty-state" style="padding: 28px">
          <span class="mdi mdi-code-json" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>{{ $t('audit.noAdditionalMetadataForThis') }}</p>
        </div>
        <div v-else class="card-body">
          <pre class="metadata-block">{{ prettyMetadata }}</pre>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.header-left { display: flex; align-items: center; gap: 12px; }
.subline { display: block; margin-top: 2px; font-size: 13px; color: var(--text-muted); }

.details {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 18px 24px;
}
.detail { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.detail > .text-muted { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.06em; }
.detail code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; }

.metadata-block {
  margin: 0;
  padding: 14px 16px;
  background: var(--bg-secondary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 13px;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
