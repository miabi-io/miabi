<script setup lang="ts">
import { ref, watch } from 'vue'
import { adminApi } from '@/api/admin'
import type { DatabaseSize, DatabaseSizeInput } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import { fmtSize } from '@/utils/format'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const entitlement = useEntitlement('database_sizes')

const sizes = ref<DatabaseSize[]>([])
const loading = ref(false)
const saving = ref(false)
const showModal = ref(false)
const editing = ref<DatabaseSize | null>(null)
const confirmTarget = ref<DatabaseSize | null>(null)

const blank = (): DatabaseSizeInput => ({ name: '', display_name: '', description: '', memory_mb: 1024, cpu_cores: 1 })
const form = ref<DatabaseSizeInput>(blank())

async function load() {
  loading.value = true
  try {
    sizes.value = (await adminApi.listDatabaseSizes()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(() => entitlement.has.value, (on) => { if (on) load() }, { immediate: true })

function cores(s: DatabaseSize): number {
  return +(s.nano_cpus / 1e9).toFixed(2)
}

function openCreate() {
  editing.value = null
  form.value = blank()
  showModal.value = true
}

function openEdit(s: DatabaseSize) {
  editing.value = s
  form.value = {
    name: s.name, display_name: s.display_name ?? '', description: s.description ?? '',
    memory_mb: Math.round(s.memory_bytes / 1048576), cpu_cores: cores(s),
  }
  showModal.value = true
}

async function save() {
  saving.value = true
  const payload = { ...form.value, name: form.value.name.trim(), memory_mb: Number(form.value.memory_mb), cpu_cores: Number(form.value.cpu_cores) }
  try {
    if (editing.value) await adminApi.updateDatabaseSize(editing.value.id, payload)
    else await adminApi.createDatabaseSize(payload)
    notify.success(editing.value ? 'Size updated' : 'Size created')
    showModal.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  const s = confirmTarget.value
  confirmTarget.value = null
  if (!s) return
  try {
    await adminApi.deleteDatabaseSize(s.id)
    notify.success('Size deleted')
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
        <h1>Database sizes</h1>
        <p class="cell-sub">Named CPU and memory limits workspaces give their databases. A plan offers some of them, the first its default.</p>
      </div>
      <button v-if="entitlement.has.value" class="btn btn-primary" :disabled="!entitlement.mutable.value" @click="openCreate">
        <span class="mdi mdi-plus"></span> New size
      </button>
    </div>

    <div v-if="!entitlement.has.value" class="card">
      <div class="empty-state" style="padding: 28px">
        <span class="mdi mdi-lock-outline" style="font-size: 32px; color: var(--text-muted)"></span>
        <h3>Enterprise</h3>
        <p class="text-muted">Database sizes need an Enterprise license with database sizes (Business and up).</p>
      </div>
    </div>

    <div v-else class="card">
      <div v-if="loading && sizes.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="sizes.length === 0" class="empty-state" style="padding: 28px">
        <span class="mdi mdi-database-cog-outline" style="font-size: 32px; color: var(--text-muted)"></span>
        <h3>No sizes yet</h3>
        <p class="text-muted">Create sizes such as small, medium and large, then offer them on a plan.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Name</th><th>CPU</th><th>Memory</th><th></th></tr></thead>
          <tbody>
            <tr v-for="s in sizes" :key="s.id">
              <td>
                <span class="cell-text">
                  <span class="cell-title">{{ s.display_name || s.name }}</span>
                  <span class="cell-sub mono">{{ s.name }}</span>
                </span>
              </td>
              <td>{{ cores(s) }}</td>
              <td>{{ fmtSize(s.memory_bytes) }}</td>
              <td style="text-align: right; white-space: nowrap">
                <button class="btn btn-ghost btn-sm" :disabled="!entitlement.mutable.value" @click="openEdit(s)">Edit</button>
                <button class="btn btn-ghost btn-sm" @click="confirmTarget = s">Delete</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit size' : 'New size' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input mono" placeholder="e.g. medium" :disabled="!!editing" required />
              <p class="form-hint">The handle manifests and the API use. It cannot be changed, since databases carry it.</p>
            </div>
            <div class="form-group">
              <label class="form-label">Display name <span class="text-muted">(optional)</span></label>
              <input v-model="form.display_name" class="form-input" placeholder="Medium" />
            </div>
            <div class="form-group">
              <label class="form-label">Description <span class="text-muted">(optional)</span></label>
              <input v-model="form.description" class="form-input" placeholder="For production workloads" />
            </div>
            <div class="flex gap-3">
              <div class="form-group" style="flex: 1">
                <label class="form-label">CPU cores</label>
                <input v-model.number="form.cpu_cores" type="number" min="0.1" step="0.25" class="form-input" required />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Memory (MB)</label>
                <input v-model.number="form.memory_mb" type="number" min="1" class="form-input" required />
              </div>
            </div>
            <p v-if="editing" class="form-hint" style="margin-bottom: 0">
              The change applies to databases given this size from now on; databases already on it keep their limits.
            </p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : editing ? 'Save' : 'Create size' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!confirmTarget"
      title="Delete size"
      :message="`&quot;${confirmTarget?.name ?? ''}&quot; will no longer be offered. Databases already on it keep their limits.`"
      confirm-label="Delete"
      variant="danger"
      @confirm="remove"
      @cancel="confirmTarget = null"
    />
  </div>
</template>
