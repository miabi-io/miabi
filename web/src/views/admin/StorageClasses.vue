<script setup lang="ts">
import BetaBadge from '@/components/BetaBadge.vue'
import { onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import { nodesApi } from '@/api/nodes'
import { useRouter } from 'vue-router'
import type { Server, StorageClass } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { fmtSize } from '@/utils/format'
import StorageClassFormModal from '@/components/StorageClassFormModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const router = useRouter()

const classes = ref<StorageClass[]>([])
const nodes = ref<Server[]>([])
const loading = ref(false)
const showModal = ref(false)
const editing = ref<StorageClass | null>(null)
const confirmTarget = ref<StorageClass | null>(null)

const nodeName = (id: number) => nodes.value.find(n => n.id === id)?.display_name || nodes.value.find(n => n.id === id)?.name || 'this node'

async function load() {
  loading.value = true
  try {
    const [list, nodeList] = await Promise.all([adminApi.listStorageClasses(), nodesApi.list()])
    classes.value = list.data.data ?? []
    nodes.value = nodeList.data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function usedPercent(c: StorageClass): number | null {
  if (!c.capacity_bytes || c.available_bytes === undefined) return null
  return Math.round(((c.capacity_bytes - c.available_bytes) / c.capacity_bytes) * 100)
}

function openCreate() {
  editing.value = null
  showModal.value = true
}

function openEdit(c: StorageClass) {
  editing.value = c
  showModal.value = true
}

async function onSaved() {
  showModal.value = false
  await load()
}

async function remove() {
  const c = confirmTarget.value
  confirmTarget.value = null
  if (!c) return
  try {
    await adminApi.deleteStorageClass(c.id)
    notify.success('Storage class deleted')
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}

</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Storage classes<BetaBadge component="storage-classes" /></h1>
        <p class="cell-sub">
          Where on a node the platform creates volumes. Workspaces choose a class by name — they never see or supply a host path.
        </p>
      </div>
      <div class="header-actions">
        <button class="btn btn-primary" @click="openCreate">
          <span class="mdi mdi-plus"></span> New storage class
        </button>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && classes.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>Class</th><th>Node</th><th>Path</th><th>Capacity</th><th>Reclaim</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="c in classes" :key="c.id" class="row-clickable" @click="router.push(`/admin/storage-classes/${c.id}`)">
              <td>
                <span class="cell-text">
                  <span class="cell-title">
                    <router-link :to="`/admin/storage-classes/${c.id}`" class="class-link" @click.stop>{{ c.display_name || c.name }}</router-link>
                    <span v-if="c.is_default" class="badge badge-info">default</span>
                    <span v-if="!c.enabled" class="badge">disabled</span>
                    <span v-if="c.shared" class="badge">shared</span>
                  </span>
                  <span class="cell-sub mono">{{ c.name }}</span>
                </span>
              </td>
              <td>{{ c.builtin ? 'Every node' : nodeName(c.server_id) }}</td>
              <td>
                <span v-if="c.path" class="mono">{{ c.path }}</span>
                <span v-else class="text-muted">Docker data directory</span>
              </td>
              <td>
                <template v-if="c.capacity_bytes">
                  {{ fmtSize(c.available_bytes ?? 0) }} free of {{ fmtSize(c.capacity_bytes) }}
                  <span class="cell-sub">{{ usedPercent(c) }}% used</span>
                </template>
                <span v-else class="text-muted">—</span>
              </td>
              <td>{{ c.builtin ? '—' : c.reclaim_policy }}</td>
              <td style="text-align: right; white-space: nowrap" @click.stop>
                <button class="btn btn-ghost btn-sm" @click="openEdit(c)">Edit</button>
                <button class="btn btn-ghost btn-sm" :disabled="c.builtin" @click="confirmTarget = c">Delete</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <StorageClassFormModal :open="showModal" :editing="editing" :nodes="nodes" @close="showModal = false" @saved="onSaved" />

    <ConfirmDialog
      :open="!!confirmTarget"
      :title="$t('confirm.title.deleteStorageClass')"
      :message="$t('confirm.message.storageClasses.nameWillNoLonger', { name: confirmTarget?.name ?? '' })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      @confirm="remove"
      @cancel="confirmTarget = null"
    />
  </div>
</template>

<style scoped>
.header-actions { display: flex; align-items: center; gap: 10px; }
.class-link { color: inherit; text-decoration: none; }
.class-link:hover { text-decoration: underline; }
</style>
