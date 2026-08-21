<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { nodesApi, type HousekeepingReport, type HousekeepingPlan, type DriftItem } from '@/api/nodes'
import type { Server } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const route = useRoute()
const notify = useNotificationStore()
const id = Number(route.params.id)

const node = ref<Server | null>(null)
const offline = ref(false)
const housekeeping = ref<HousekeepingReport | null>(null)
const hkLoading = ref(false)

// Selection state.
const hkReclaim = ref({ dangling_images: false, build_cache: false })
const hkOrphans = ref<Set<string>>(new Set()) // selected orphan keys "kind:ref"
const hkPlan = ref<HousekeepingPlan | null>(null)
const hkBusy = ref(false)
const showHkPlan = ref(false)

const connected = computed(() => !!node.value && (node.value.is_local || !!node.value.agent_connected))

onMounted(load)

async function load() {
  hkLoading.value = true
  offline.value = false
  try {
    const list = (await nodesApi.list()).data.data ?? []
    node.value = list.find((n) => n.id === id) ?? null
    if (!node.value) {
      notify.error('Node not found')
      return
    }
    if (!connected.value) {
      offline.value = true
      return
    }
    await loadHousekeeping()
  } catch (e) {
    notify.apiError(e)
  } finally {
    hkLoading.value = false
  }
}

function orphanKey(o: DriftItem): string { return `${o.kind}:${o.ref}` }

async function loadHousekeeping() {
  hkLoading.value = true
  try {
    housekeeping.value = (await nodesApi.housekeeping(id)).data.data
    // Drop selections that no longer correspond to current orphans.
    const live = new Set((housekeeping.value?.drift.orphans ?? []).map(orphanKey))
    hkOrphans.value = new Set([...hkOrphans.value].filter((k) => live.has(k)))
  } catch (e) {
    notify.apiError(e)
  } finally {
    hkLoading.value = false
  }
}

function toggleOrphan(o: DriftItem) {
  const k = orphanKey(o)
  const next = new Set(hkOrphans.value)
  next.has(k) ? next.delete(k) : next.add(k)
  hkOrphans.value = next
}

const hkSelectedOrphanList = computed<DriftItem[]>(() =>
  (housekeeping.value?.drift.orphans ?? []).filter((o) => hkOrphans.value.has(orphanKey(o))),
)
const diskCats = computed(() => {
  const d = housekeeping.value?.disk
  if (!d) return []
  return [
    { key: 'images', label: 'Images', total: d.images.total_bytes, reclaimable: d.images.reclaimable_bytes, count: d.images.count },
    { key: 'volumes', label: 'Volumes', total: d.volumes.total_bytes, reclaimable: d.volumes.reclaimable_bytes, count: d.volumes.count },
    { key: 'build_cache', label: 'Build cache', total: d.build_cache.total_bytes, reclaimable: d.build_cache.reclaimable_bytes, count: d.build_cache.count },
    { key: 'containers', label: 'Containers', total: d.containers.total_bytes, reclaimable: d.containers.reclaimable_bytes, count: d.containers.count },
  ]
})
const driftCount = computed(() => {
  const dr = housekeeping.value?.drift
  return dr ? dr.orphans.length + dr.missing.length + dr.untracked.length : 0
})
const hkHasSelection = computed(() =>
  hkReclaim.value.dangling_images || hkReclaim.value.build_cache || hkOrphans.value.size > 0,
)

function hkSelection() {
  return {
    reclaim: { dangling_images: hkReclaim.value.dangling_images, build_cache: hkReclaim.value.build_cache },
    orphans: hkSelectedOrphanList.value.map((o) => ({ kind: o.kind, ref: o.ref })),
  }
}

// Preview: dry-run the selection and open the confirm modal with the itemized plan.
async function previewHousekeeping() {
  if (!hkHasSelection.value) return
  hkBusy.value = true
  try {
    hkPlan.value = (await nodesApi.housekeepingPlan(id, hkSelection())).data.data
    showHkPlan.value = true
  } catch (e) {
    notify.apiError(e)
  } finally {
    hkBusy.value = false
  }
}

// Apply the previewed selection, then refresh the report.
async function applyHousekeeping() {
  hkBusy.value = true
  try {
    const res = (await nodesApi.housekeepingApply(id, hkSelection())).data.data
    const freed = (res.images_reclaimed_bytes || 0) + (res.build_cache_reclaimed_bytes || 0)
    const parts: string[] = []
    if (freed > 0) parts.push(`freed ${fmtSize(freed)}`)
    if (res.orphans_removed?.length) parts.push(`removed ${res.orphans_removed.length} orphan(s)`)
    notify.success(parts.length ? `Housekeeping: ${parts.join(', ')}` : 'Nothing to reclaim')
    if (res.errors?.length) res.errors.forEach((m: string) => notify.error(m))
    showHkPlan.value = false
    hkReclaim.value = { dangling_images: false, build_cache: false }
    hkOrphans.value = new Set()
    await loadHousekeeping()
  } catch (e) {
    notify.apiError(e)
  } finally {
    hkBusy.value = false
  }
}

function fmtSize(n?: number): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}
</script>

<template>
  <div class="node-housekeeping">
    <div class="page-header">
      <div>
        <router-link :to="`/admin/nodes/${id}`" class="back-link"><span class="mdi mdi-arrow-left"></span> Node</router-link>
        <h1>Housekeeping <span v-if="node" class="text-muted" style="font-weight: 400; font-size: 16px">— {{ node.name }}</span></h1>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" :disabled="hkLoading || offline" @click="loadHousekeeping"><span class="mdi mdi-refresh"></span> Refresh</button>
      </div>
    </div>

    <div v-if="hkLoading && !housekeeping && !offline" class="card"><div class="card-body"><span class="spinner"></span> Analyzing node…</div></div>

    <div v-else-if="offline" class="card"><div class="card-body text-muted">This node is offline — housekeeping is unavailable until its agent reconnects.</div></div>

    <template v-else-if="housekeeping">
      <!-- Disk usage -->
      <div class="card mb-4">
        <div class="card-header"><h2>Disk usage</h2></div>
        <div class="hk-grid">
          <div v-for="cat in diskCats" :key="cat.key" class="hk-disk">
            <span class="hk-disk-label">{{ cat.label }}</span>
            <span class="hk-disk-value">{{ fmtSize(cat.total) }}</span>
            <span class="hk-disk-sub">{{ fmtSize(cat.reclaimable) }} reclaimable · {{ cat.count }} item(s)</span>
          </div>
        </div>
      </div>

      <!-- Reclaim -->
      <div class="card mb-4">
        <div class="card-header"><h2>Reclaim disk</h2></div>
        <div class="hk-section">
          <label class="hk-check">
            <input v-model="hkReclaim.dangling_images" type="checkbox" :disabled="!housekeeping.reclaim.dangling_images.count" />
            Dangling images
            <span class="text-muted">{{ housekeeping.reclaim.dangling_images.count }} · {{ fmtSize(housekeeping.reclaim.dangling_images.bytes) }}</span>
          </label>
          <label class="hk-check">
            <input v-model="hkReclaim.build_cache" type="checkbox" :disabled="!housekeeping.reclaim.build_cache.bytes" />
            Build cache
            <span class="text-muted">{{ fmtSize(housekeeping.reclaim.build_cache.bytes) }}</span>
          </label>
        </div>
      </div>

      <!-- Drift -->
      <div class="card mb-4">
        <div class="card-header"><h2>Drift <span class="text-muted" style="font-weight: 400">{{ driftCount }} item(s)</span></h2></div>
        <div v-if="driftCount === 0" class="empty-state" style="padding: 24px"><p class="text-muted">No drift — Docker matches Miabi. ✓</p></div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th></th><th>Resource</th><th>Type</th><th>Class</th><th>Recommended action</th></tr></thead>
            <tbody>
              <tr v-for="o in housekeeping.drift.orphans" :key="'o-' + o.kind + o.ref">
                <td><input type="checkbox" :checked="hkOrphans.has(orphanKey(o))" @change="toggleOrphan(o)" /></td>
                <td class="trunc mono" :title="o.name">{{ o.name }}<div v-if="o.image" class="cell-sub">{{ o.image }}</div></td>
                <td class="cell-sub">{{ o.kind }}</td>
                <td><span class="badge badge-danger">orphan</span></td>
                <td class="cell-sub">remove <span class="text-muted" v-if="o.owner_kind">({{ o.owner_kind }} #{{ o.owner_id }} deleted in Miabi)</span></td>
              </tr>
              <tr v-for="o in housekeeping.drift.missing" :key="'m-' + o.ref">
                <td></td>
                <td class="trunc" :title="o.name">{{ o.name }}</td>
                <td class="cell-sub">{{ o.kind }}</td>
                <td><span class="badge badge-warning">missing</span></td>
                <td class="cell-sub">redeploy from its app</td>
              </tr>
              <tr v-for="o in housekeeping.drift.untracked" :key="'u-' + o.ref">
                <td></td>
                <td class="trunc mono" :title="o.name">{{ o.name }}<div v-if="o.image" class="cell-sub">{{ o.image }}</div></td>
                <td class="cell-sub">{{ o.kind }}</td>
                <td><span class="badge badge-info">untracked</span></td>
                <td class="cell-sub">
                  <router-link :to="`/admin/nodes/${id}/import`">import</router-link>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="hk-actions">
          <span class="text-muted" style="font-size: 12px">Dry-run first · platform-managed resources are never touched.</span>
          <button class="btn btn-primary" :disabled="!hkHasSelection || hkBusy" @click="previewHousekeeping">
            <span class="mdi mdi-broom"></span> Preview &amp; reclaim
          </button>
        </div>
      </div>
    </template>

    <Teleport to="body">
      <AppModal v-if="showHkPlan && hkPlan" @close="showHkPlan = false">
        <div class="modal-header">
          <h3>Confirm housekeeping</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showHkPlan = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p class="text-muted" style="font-size: 13px; margin-bottom: 10px">This dry-run is exactly what will be reclaimed and removed.</p>
          <ul class="hk-plan">
            <li v-if="hkPlan.reclaim.dangling_images">Prune {{ hkPlan.dangling_images.count }} dangling image(s) — {{ fmtSize(hkPlan.dangling_images.bytes) }}</li>
            <li v-if="hkPlan.reclaim.build_cache">Prune build cache — {{ fmtSize(hkPlan.build_cache.bytes) }}</li>
            <li v-for="o in hkPlan.orphans" :key="'p-' + o.kind + o.ref">Remove orphan {{ o.kind }} <strong>{{ o.name }}</strong></li>
            <li v-if="!hkPlan.reclaim.dangling_images && !hkPlan.reclaim.build_cache && hkPlan.orphans.length === 0" class="text-muted">Nothing selected.</li>
          </ul>
          <p style="margin-top: 12px; font-weight: 600">Estimated reclaim: {{ fmtSize(hkPlan.estimated_bytes) }}</p>
          <p class="text-muted" style="font-size: 12px; margin-top: 6px">Orphan removal is irreversible. Platform-managed apps, databases and the gateway are never affected.</p>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="showHkPlan = false">Cancel</button>
          <button type="button" class="btn btn-danger" :disabled="hkBusy" @click="applyHousekeeping">{{ hkBusy ? 'Working…' : 'Reclaim now' }}</button>
        </div>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.page-header { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; margin-bottom: 20px; }
.header-actions { display: flex; gap: 8px; }
.back-link { display: inline-flex; align-items: center; gap: 4px; color: var(--text-muted); font-size: 13px; text-decoration: none; margin-bottom: 4px; }
.back-link:hover { color: var(--text); }

/* Housekeeping */
.hk-grid {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px; padding: 16px 20px;
}
.hk-disk {
  display: flex; flex-direction: column; gap: 2px;
  padding: 12px 14px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-subtle, transparent);
}
.hk-disk-label { font-size: 12px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; }
.hk-disk-value { font-size: 20px; font-weight: 600; }
.hk-disk-sub { font-size: 12px; color: var(--text-muted); }
.hk-section { padding: 12px 20px 16px; }
.hk-check { display: flex; align-items: center; gap: 8px; font-size: 14px; padding: 4px 0; cursor: pointer; }
.hk-check input:disabled { cursor: not-allowed; }
.hk-actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 20px; border-top: 1px solid var(--border); }
.hk-plan { margin: 0; padding-left: 18px; font-size: 13px; line-height: 1.7; }
</style>
