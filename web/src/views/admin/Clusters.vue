<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { clustersApi } from '@/api/clusters'
import type { Cluster } from '@/api/types'

const notify = useNotificationStore()
const router = useRouter()

const clusters = ref<Cluster[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    clusters.value = (await clustersApi.list()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Clusters</h1>
        <p class="cell-sub">Every node belongs to exactly one cluster. Tenants see a cluster as a location.</p>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && clusters.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Name</th><th>Location code</th><th>Mode</th><th>Nodes</th></tr></thead>
          <tbody>
            <tr v-for="c in clusters" :key="c.id" class="row-clickable" @click="router.push(`/admin/clusters/${c.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-lan" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">
                      {{ c.display_name || c.name }}
                      <span v-if="c.is_default" class="badge badge-info" style="margin-left: 8px">default</span>
                      <span
                        v-if="c.legacy_ingress"
                        class="badge badge-warning"
                        style="margin-left: 8px"
                        title="Converted from a port-forward node: the central gateway still reaches its apps by host port"
                      >legacy ingress</span>
                    </span>
                    <span class="cell-sub mono">{{ c.name }}</span>
                  </span>
                </div>
              </td>
              <td>
                <span v-if="c.location_code" class="badge badge-muted mono">{{ c.location_code }}</span>
                <span v-else class="cell-sub">—</span>
              </td>
              <td>
                <span class="badge" :class="c.mode === 'swarm' ? 'badge-success' : 'badge-muted'">
                  {{ c.mode === 'swarm' ? 'Docker Swarm' : 'standalone' }}
                </span>
              </td>
              <td class="cell-sub">{{ c.node_count }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mono {
  font-family: var(--font-mono, monospace);
}
</style>
