<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import { nodesApi } from '@/api/nodes'
import type { Server, StorageClass, StorageClassInput } from '@/api/types'
import { apiError as decodeApiError } from '@/api/client'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { fmtSize } from '@/utils/format'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
// Every edition may register disks, up to an edition cap on the catalog that counts the built-in
// class. Enterprise lifts the cap.
const license = useLicenseStore()

const classes = ref<StorageClass[]>([])
const nodes = ref<Server[]>([])
const loading = ref(false)
const saving = ref(false)
const showModal = ref(false)
const editing = ref<StorageClass | null>(null)
const confirmTarget = ref<StorageClass | null>(null)

const blank = (): StorageClassInput => ({
  name: '', display_name: '', description: '', server_id: 0, path: '',
  shared: false, is_default: false, enabled: true, reclaim_policy: 'delete',
})
const form = ref<StorageClassInput>(blank())

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
onMounted(() => {
  void load()
  // Idempotent; drives the class-cap gate.
  void license.load()
})

const usage = computed(() => license.view?.storage_class_usage)
const limited = computed(() => (usage.value?.limit ?? -1) >= 0)
const atCap = computed(() => {
  const u = usage.value
  return !!u && u.limit >= 0 && u.used >= u.limit
})
const capTitle = computed(() =>
  atCap.value ? `Your edition allows ${usage.value?.limit} storage classes — upgrade your license to add more disks` : '',
)

function usedPercent(c: StorageClass): number | null {
  if (!c.capacity_bytes || c.available_bytes === undefined) return null
  return Math.round(((c.capacity_bytes - c.available_bytes) / c.capacity_bytes) * 100)
}

function openCreate() {
  editing.value = null
  form.value = blank()
  showModal.value = true
}

function openEdit(c: StorageClass) {
  editing.value = c
  form.value = {
    name: c.name, display_name: c.display_name, description: c.description ?? '', server_id: c.server_id,
    path: c.path, shared: c.shared, is_default: c.is_default, enabled: c.enabled,
    reclaim_policy: c.reclaim_policy,
  }
  showModal.value = true
}

async function save() {
  saving.value = true
  try {
    if (editing.value) {
      const { name: _name, path: _path, ...editable } = form.value
      await adminApi.updateStorageClass(editing.value.id, editable)
    } else {
      await adminApi.createStorageClass({ ...form.value, name: form.value.name.trim(), path: form.value.path.trim() })
    }
    notify.success(editing.value ? 'Storage class updated' : 'Storage class registered')
    showModal.value = false
    await load()
  } catch (e) {
    if (decodeApiError(e).code === 'STORAGE_CLASS_LIMIT_REACHED') {
      // The count is read from the license view, so refresh it: the cap was hit with a
      // stale one.
      void license.load(true)
      notify.error(`Your edition allows ${usage.value?.limit ?? 2} storage classes, the built-in one included.`, {
        title: 'Storage class limit reached',
      })
    } else {
      notify.apiError(e)
    }
  } finally {
    saving.value = false
  }
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

const reclaimHint = computed(() =>
  form.value.reclaim_policy === 'delete'
    ? "Deleting a volume also removes its directory from this disk. Without it, the data would stay behind after the volume is gone."
    : 'Deleting a volume keeps its directory on the disk for you to reclaim by hand.',
)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Storage classes</h1>
        <p class="cell-sub">
          Where on a node the platform creates volumes. Workspaces choose a class by name — they never see or supply a host path.
        </p>
      </div>
      <div class="header-actions">
        <span
          v-if="limited"
          class="class-usage"
          :class="{ 'class-usage--full': atCap }"
          :title="atCap ? capTitle : 'Storage classes used of your edition limit, the built-in one included'"
        >
          <span class="mdi mdi-harddisk"></span> {{ usage?.used ?? 0 }} / {{ usage?.limit }} classes
        </span>
        <button class="btn btn-primary" :disabled="atCap" :title="capTitle" @click="openCreate">
          <span class="mdi mdi-plus"></span> New storage class
        </button>
      </div>
    </div>

    <div v-if="atCap" class="cap-note">
      <span class="mdi mdi-lock-outline"></span>
      <span>
        Your edition includes <strong>{{ usage?.limit }}</strong> storage classes, the built-in one included. Enterprise
        lifts the cap; classes already registered keep working either way.
      </span>
      <router-link to="/admin/license" class="cap-link">Manage license</router-link>
    </div>

    <div class="card">
      <div v-if="loading && classes.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>Class</th><th>Node</th><th>Path</th><th>Capacity</th><th>Reclaim</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="c in classes" :key="c.id">
              <td>
                <span class="cell-text">
                  <span class="cell-title">
                    {{ c.display_name || c.name }}
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
              <td style="text-align: right; white-space: nowrap">
                <button class="btn btn-ghost btn-sm" @click="openEdit(c)">Edit</button>
                <button class="btn btn-ghost btn-sm" :disabled="c.builtin" @click="confirmTarget = c">Delete</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit storage class' : 'New storage class' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input mono" placeholder="e.g. ssd-fast" :disabled="!!editing" required />
              <p class="form-hint">
                A short lowercase handle workspaces and manifests ask for. It can never be changed — volumes and GitOps
                manifests reference it.
              </p>
            </div>
            <div class="form-group">
              <label class="form-label">Display name</label>
              <input v-model="form.display_name" class="form-input" placeholder="NVMe (ssd1)" />
              <p class="form-hint">Shown to workspaces instead of the path. Safe to change at any time.</p>
            </div>
            <div class="form-group">
              <label class="form-label">Description <span class="text-muted">(optional)</span></label>
              <input v-model="form.description" class="form-input" placeholder="Fast NVMe for databases" />
            </div>
            <div class="form-group" v-if="!editing">
              <label class="form-label">Node</label>
              <select v-model.number="form.server_id" class="form-input">
                <option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.display_name || n.name }}</option>
              </select>
            </div>
            <div class="form-group">
              <label class="form-label">Path</label>
              <input v-model="form.path" class="form-input mono" placeholder="/mnt/ssd1/miabi" :disabled="!!editing" required />
              <p class="form-hint">
                A directory Miabi owns on that node; each volume gets its own subdirectory. It is checked before the class is saved,
                so an unmounted disk fails here rather than at someone's first deploy. It cannot be changed afterwards — existing
                volumes hold their data under it.
              </p>
            </div>
            <div class="form-group">
              <label class="form-label">Reclaim policy</label>
              <select v-model="form.reclaim_policy" class="form-input">
                <option value="delete">Delete the directory with the volume</option>
                <option value="retain">Keep the directory</option>
              </select>
              <p class="form-hint">{{ reclaimHint }}</p>
            </div>
            <label class="checkbox-row">
              <input v-model="form.enabled" type="checkbox" />
              <span>Enabled <span class="text-muted">— off blocks new volumes here; existing ones keep working</span></span>
            </label>
            <label class="checkbox-row">
              <input v-model="form.is_default" type="checkbox" />
              <span>Default for this node <span class="text-muted">— used when a volume names no class</span></span>
            </label>
            <label class="checkbox-row">
              <input v-model="form.shared" type="checkbox" :disabled="!!editing && editing.builtin" />
              <span>
                Shared across the cluster
                <span class="text-muted">— the same filesystem is mounted at this path on every node</span>
              </span>
            </label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">
              {{ saving ? 'Saving…' : editing ? 'Save' : 'Register class' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

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
.class-usage {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-muted);
  background: var(--surface-2, rgba(127, 127, 127, 0.1));
}
.class-usage--full { color: var(--warning, #b7791f); background: var(--warning-bg, rgba(183, 121, 31, 0.12)); }
.cap-note { display: flex; align-items: center; gap: 8px; margin-bottom: 16px; padding: 10px 14px; border-radius: 8px; font-size: 13px; background: var(--warning-bg, rgba(245, 158, 11, 0.1)); color: var(--warning, #b45309); }
.cap-note .mdi { font-size: 16px; }
.cap-link { margin-left: auto; font-weight: 600; white-space: nowrap; color: inherit; text-decoration: none; }
</style>
