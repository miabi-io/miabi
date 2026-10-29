<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import type { StorageClassDetail, StorageClassUsage } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { fmtSize } from '@/utils/format'
import { fmtDate } from '@/utils/datetime'
import { relativeTime } from '@/utils/time'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StorageClassFormModal from '@/components/StorageClassFormModal.vue'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()

const detail = ref<StorageClassDetail | null>(null)
const loading = ref(false)
const filter = ref<'all' | 'volume' | 'database'>('all')
const editing = ref(false)
const confirmDelete = ref(false)

async function load() {
  loading.value = true
  try {
    const r = await adminApi.getStorageClass(Number(route.params.id))
    detail.value = { ...r.data.data, usage: r.data.data.usage ?? [] }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(() => route.params.id, () => { if (route.params.id) void load() }, { immediate: true })

function pct(capacity?: number, available?: number): number | null {
  if (!capacity) return null
  return Math.min(100, Math.max(0, Math.round(((capacity - (available ?? 0)) / capacity) * 100)))
}
const usedPct = computed(() => pct(detail.value?.capacity_bytes, detail.value?.available_bytes))
// Data on the class as the usage sweeps measured it; the rest of the used space belongs to other
// things on the same disk (images, containers, other classes or files).
const ownPct = computed(() => {
  const d = detail.value
  if (!d?.capacity_bytes) return 0
  return Math.min(usedPct.value ?? 0, Math.round((d.used_bytes / d.capacity_bytes) * 100))
})

const rows = computed<StorageClassUsage[]>(() =>
  (detail.value?.usage ?? []).filter(u => filter.value === 'all' || u.kind === filter.value),
)
async function onSaved() {
  editing.value = false
  await load()
}

async function remove() {
  confirmDelete.value = false
  if (!detail.value) return
  try {
    await adminApi.deleteStorageClass(detail.value.id)
    notify.success('Storage class deleted')
    router.push('/admin/storage-classes')
  } catch (e) {
    notify.apiError(e)
  }
}

const unmeasured = computed(() => (detail.value?.usage ?? []).filter(u => !u.measured_at).length)
</script>

<template>
  <div>
    <div v-if="loading && !detail" class="loading-page"><span class="spinner"></span></div>

    <template v-else-if="detail">
      <div class="page-header">
        <div class="header-left">
          <button class="btn-icon btn-icon-muted" title="Back to storage classes" aria-label="Back to storage classes" @click="router.push('/admin/storage-classes')">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <div class="header-title">
            <h1>
              {{ detail.display_name || detail.name }}
              <span v-if="detail.is_default" class="badge badge-info">default</span>
              <span v-if="detail.builtin" class="badge">built-in</span>
              <span v-if="detail.shared" class="badge">shared</span>
              <span v-if="detail.enabled" class="badge badge-dot badge-success">enabled</span>
              <span v-else class="badge badge-dot badge-danger">disabled</span>
            </h1>
            <span class="subline">{{ detail.description || 'No description' }}</span>
          </div>
        </div>
        <div class="sc-actions">
          <button class="btn btn-secondary" @click="editing = true"><span class="mdi mdi-pencil-outline"></span> Edit</button>
          <button
            class="btn btn-danger"
            :disabled="detail.builtin || detail.usage.length > 0"
            :title="detail.builtin ? 'The built-in class cannot be deleted' : detail.usage.length ? 'Volumes or databases still use this class — disable it instead' : ''"
            @click="confirmDelete = true"
          >
            <span class="mdi mdi-delete-outline"></span> Delete
          </button>
        </div>
      </div>

      <div class="sc-grid">
        <div class="card">
          <div class="card-header"><h3>Capacity</h3></div>
          <div class="card-body">
            <template v-if="detail.capacity_bytes">
              <div class="sc-figure">
                <strong>{{ fmtSize(detail.available_bytes ?? 0) }}</strong> free of {{ fmtSize(detail.capacity_bytes) }}
                <span class="text-muted">· {{ usedPct }}% used</span>
              </div>
              <div class="sc-meter" role="img" :aria-label="`${usedPct}% of the disk used, ${ownPct}% by data on this class`">
                <div class="sc-meter-own" :style="{ width: ownPct + '%' }"></div>
                <div class="sc-meter-other" :style="{ width: Math.max(0, (usedPct ?? 0) - ownPct) + '%' }"></div>
              </div>
              <div class="sc-legend">
                <span><i class="sc-dot sc-dot-own"></i> This class: {{ fmtSize(detail.used_bytes) }}</span>
                <span><i class="sc-dot sc-dot-other"></i> Other data on the same disk</span>
              </div>
              <p class="cell-sub">
                <template v-if="detail.builtin">Sum of every node's Docker data disk, measured by the node capacity sweep</template>
                <template v-else>Measured by the storage sweep</template>
                {{ detail.measured_at ? relativeTime(detail.measured_at) : '' }}.
              </p>
            </template>
            <p v-else class="text-muted">
              Not measured yet. {{ detail.builtin ? 'The node capacity sweep sizes each node’s Docker data disk.' : 'The storage sweep sizes the disk once it can reach the node.' }}
            </p>
          </div>
        </div>

        <div class="card">
          <div class="card-header"><h3>Details</h3></div>
          <div class="card-body">
            <dl class="sc-kv">
              <dt>Name</dt><dd class="mono">{{ detail.name }}</dd>
              <dt>Node</dt><dd>{{ detail.builtin ? 'Every node' : (detail.server_name || `Node ${detail.server_id}`) }}</dd>
              <dt>Path</dt><dd><span v-if="detail.path" class="mono">{{ detail.path }}</span><span v-else class="text-muted">Docker data directory</span></dd>
              <dt>Reclaim</dt><dd>{{ detail.builtin ? '—' : detail.reclaim_policy === 'delete' ? 'Delete the directory with the volume' : 'Keep the directory' }}</dd>
              <dt>Holds</dt><dd>{{ detail.volumes }} volume{{ detail.volumes === 1 ? '' : 's' }}, {{ detail.databases }} database{{ detail.databases === 1 ? '' : 's' }}</dd>
              <dt>Registered</dt><dd>{{ fmtDate(detail.created_at) }}</dd>
            </dl>
          </div>
        </div>
      </div>

      <div v-if="detail.nodes?.length" class="card">
        <div class="card-header"><h3>Disks by node</h3></div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Node</th><th>Free</th><th>Size</th><th>Used</th><th>Measured</th></tr></thead>
            <tbody>
              <tr v-for="n in detail.nodes" :key="n.server_id">
                <td>
                  <router-link :to="`/admin/nodes/${n.server_id}`" class="sc-link">{{ n.server_name }}</router-link>
                  <span v-if="n.is_local" class="badge">control plane</span>
                </td>
                <template v-if="n.capacity_bytes">
                  <td>{{ fmtSize(n.available_bytes) }}</td>
                  <td>{{ fmtSize(n.capacity_bytes) }}</td>
                  <td>
                    <div class="sc-meter sc-meter-sm"><div class="sc-meter-own" :class="{ 'sc-warn': (pct(n.capacity_bytes, n.available_bytes) ?? 0) >= 85 }" :style="{ width: pct(n.capacity_bytes, n.available_bytes) + '%' }"></div></div>
                    <span class="cell-sub">{{ pct(n.capacity_bytes, n.available_bytes) }}%</span>
                  </td>
                  <td>{{ relativeTime(n.measured_at) }}</td>
                </template>
                <td v-else colspan="4" class="text-muted">Not measured — the node may be offline</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card">
        <div class="card-header sc-usage-header">
          <h3>Data on this class</h3>
          <div class="sc-filter" role="group" aria-label="Filter by kind">
            <button class="btn btn-ghost btn-sm" :class="{ active: filter === 'all' }" @click="filter = 'all'">All ({{ detail.usage.length }})</button>
            <button class="btn btn-ghost btn-sm" :class="{ active: filter === 'volume' }" @click="filter = 'volume'">Volumes ({{ detail.volumes }})</button>
            <button class="btn btn-ghost btn-sm" :class="{ active: filter === 'database' }" @click="filter = 'database'">Databases ({{ detail.databases }})</button>
          </div>
        </div>
        <div v-if="!rows.length" class="card-body text-muted">Nothing is stored on this class yet.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Name</th><th>Workspace</th><th>Node</th><th>Used</th><th>Created</th></tr></thead>
            <tbody>
              <tr v-for="u in rows" :key="u.kind + u.id">
                <td>
                  <span class="cell-text">
                    <span class="cell-title">
                      <span class="mdi" :class="u.kind === 'database' ? 'mdi-database' : 'mdi-harddisk'" aria-hidden="true"></span>
                      {{ u.display_name || u.name }}
                    </span>
                    <span class="cell-sub mono">{{ u.name }}</span>
                  </span>
                </td>
                <td>
                  <router-link v-if="u.workspace_name" :to="`/admin/workspaces/${u.workspace_id}`" class="sc-link">{{ u.workspace_name }}</router-link>
                  <span v-else class="text-muted">—</span>
                </td>
                <td>{{ u.server_name || '—' }}</td>
                <td>
                  <template v-if="u.measured_at">{{ fmtSize(u.used_bytes) }}</template>
                  <span v-else class="text-muted" title="Not measured yet">—</span>
                </td>
                <td>{{ fmtDate(u.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="unmeasured" class="card-body cell-sub">
          {{ unmeasured }} item{{ unmeasured === 1 ? ' has' : 's have' }} not been measured yet; sizes come from the usage sweeps, never from a live scan.
        </p>
      </div>
      <StorageClassFormModal :open="editing" :editing="detail" @close="editing = false" @saved="onSaved" />
      <ConfirmDialog
        :open="confirmDelete"
        :title="$t('confirm.title.deleteStorageClass')"
        :message="$t('confirm.message.storageClasses.nameWillNoLonger', { name: detail.name })"
        :confirm-label="$t('action.delete')"
        variant="danger"
        @confirm="remove"
        @cancel="confirmDelete = false"
      />
    </template>
  </div>
</template>

<style scoped>
.sc-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.sc-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 16px; margin-bottom: 16px; }
.card + .card { margin-top: 16px; }
.sc-figure { font-size: 15px; margin-bottom: 10px; }
.sc-meter { display: flex; height: 10px; border-radius: 5px; overflow: hidden; background: var(--surface-2, rgba(127, 127, 127, 0.15)); margin-bottom: 8px; }
.sc-meter-sm { height: 6px; width: 120px; display: inline-flex; vertical-align: middle; margin: 0 8px 0 0; }
.sc-meter-own { background: var(--primary-500, #6d4aff); }
.sc-meter-other { background: var(--primary-200, rgba(109, 74, 255, 0.35)); }
.sc-warn { background: var(--warning, #d97706); }
.sc-legend { display: flex; flex-wrap: wrap; gap: 14px; font-size: 13px; color: var(--text-muted); margin-bottom: 8px; }
.sc-dot { display: inline-block; width: 9px; height: 9px; border-radius: 2px; margin-right: 4px; vertical-align: middle; }
.sc-dot-own { background: var(--primary-500, #6d4aff); }
.sc-dot-other { background: var(--primary-200, rgba(109, 74, 255, 0.35)); }
.sc-kv { display: grid; grid-template-columns: max-content 1fr; gap: 8px 16px; margin: 0; font-size: 14px; }
.sc-kv dt { color: var(--text-muted); }
.sc-kv dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.sc-link { color: inherit; text-decoration: none; }
.sc-link:hover { text-decoration: underline; }
.sc-usage-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.sc-filter { display: flex; gap: 4px; flex-wrap: wrap; }
.sc-filter .active { background: var(--surface-2, rgba(127, 127, 127, 0.15)); }
</style>
