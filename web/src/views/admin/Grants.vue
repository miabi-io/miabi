<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { capabilityApi } from '@/api/capabilities'
import { useNotificationStore } from '@/stores/notification'
import type { GrantedApp } from '@/api/types'

// A grant is otherwise visible only inside the app that holds it.
const notify = useNotificationStore()

const rows = ref<GrantedApp[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    rows.value = (await capabilityApi.granted()).data.data ?? []
  } catch (e) {
    notify.apiError(e, 'Failed to load granted applications')
  } finally {
    loading.value = false
  }
}
onMounted(load)

const elevated = computed(() => rows.value.filter((r) => r.elevated).length)
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Kernel grants</h1>
      <button class="btn btn-secondary" :disabled="loading" @click="load">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span> Refresh
      </button>
    </div>

    <div class="card">
      <div class="card-body">
        <p v-if="!rows.length && !loading" class="text-muted empty-line">
          No application holds an extra capability or host device. This is the state to expect:
          a grant is asked for per application and only in a privileged workspace.
        </p>

        <table v-else class="table">
          <thead>
            <tr>
              <th>Application</th>
              <th>Workspace</th>
              <th>Capabilities</th>
              <th>Devices</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.application_id">
              <td>
                <RouterLink :to="`/apps/${r.application_id}?tab=settings`">{{ r.application_name }}</RouterLink>
                <span v-if="r.elevated" class="badge badge-warning" style="margin-left: 8px">elevated</span>
              </td>
              <td>
                <RouterLink :to="`/admin/workspaces/${r.workspace_id}`">
                  {{ r.workspace_name || `workspace ${r.workspace_id}` }}
                </RouterLink>
              </td>
              <td>
                <code v-for="c in r.add_capabilities ?? []" :key="c" class="grant-chip">{{ c }}</code>
                <span v-if="!r.add_capabilities?.length" class="text-muted">—</span>
              </td>
              <td>
                <code v-for="d in r.devices ?? []" :key="d" class="grant-chip">{{ d }}</code>
                <span v-if="!r.devices?.length" class="text-muted">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <p v-if="elevated" class="text-muted footnote">
      {{ elevated }} of these hold an elevated grant — a capability or device close enough to full host
      access that only the system workspace may hold it. Worth knowing where they are.
    </p>
  </div>
</template>

<style scoped>
.empty-line {
  margin: 0;
  font-size: 14px;
}

.grant-chip {
  display: inline-block;
  margin: 0 4px 4px 0;
  padding: 1px 6px;
  border-radius: var(--radius-sm);
  background: var(--bg-tertiary);
  font-size: 12px;
}

.footnote {
  font-size: 13px;
  margin: 16px 0 24px;
}
</style>
