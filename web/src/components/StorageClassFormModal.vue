<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { adminApi } from '@/api/admin'
import type { Server, StorageClass, StorageClassInput } from '@/api/types'
import { apiError as decodeApiError } from '@/api/client'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import AppModal from '@/components/AppModal.vue'

// Create/edit modal for a storage class, shared by the list and the detail page. Name, node and path
// are set on create only: volumes and manifests point at them.
const props = defineProps<{ open: boolean; editing: StorageClass | null; nodes?: Server[] }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const notify = useNotificationStore()
const license = useLicenseStore()
const saving = ref(false)

const blank = (): StorageClassInput => ({
  name: '', display_name: '', description: '', server_id: 0, path: '',
  shared: false, is_default: false, enabled: true, reclaim_policy: 'delete',
})
const form = ref<StorageClassInput>(blank())

watch(() => props.open, open => {
  if (!open) return
  const c = props.editing
  form.value = c
    ? {
        name: c.name, display_name: c.display_name, description: c.description ?? '', server_id: c.server_id,
        path: c.path, shared: c.shared, is_default: c.is_default, enabled: c.enabled, reclaim_policy: c.reclaim_policy,
      }
    : blank()
}, { immediate: true })

async function save() {
  saving.value = true
  try {
    if (props.editing) {
      const { name: _name, path: _path, ...editable } = form.value
      await adminApi.updateStorageClass(props.editing.id, editable)
    } else {
      await adminApi.createStorageClass({ ...form.value, name: form.value.name.trim(), path: form.value.path.trim() })
    }
    notify.success(props.editing ? 'Storage class updated' : 'Storage class registered')
    emit('saved')
  } catch (e) {
    if (decodeApiError(e).code === 'STORAGE_CLASS_LIMIT_REACHED') {
      // The count is read from the license view, so refresh it: the cap was hit with a stale one.
      void license.load(true)
      notify.error(`Your edition allows ${license.view?.storage_class_usage?.limit ?? 2} storage classes, the built-in one included.`, {
        title: 'Storage class limit reached',
      })
    } else {
      notify.apiError(e)
    }
  } finally {
    saving.value = false
  }
}

const reclaimHint = computed(() =>
  form.value.reclaim_policy === 'delete'
    ? "Deleting a volume also removes its directory from this disk. Without it, the data would stay behind after the volume is gone."
    : 'Deleting a volume keeps its directory on the disk for you to reclaim by hand.',
)
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="open" @close="emit('close')">
      <div class="modal-header">
        <h3>{{ editing ? 'Edit storage class' : 'New storage class' }}</h3>
        <button class="btn-icon btn-icon-muted" aria-label="Close" @click="emit('close')"><span class="mdi mdi-close"></span></button>
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
              <option v-for="n in nodes ?? []" :key="n.id" :value="n.id">{{ n.display_name || n.name }}</option>
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
          <button type="button" class="btn btn-secondary" @click="emit('close')">Cancel</button>
          <button type="submit" class="btn btn-primary" :disabled="saving">
            {{ saving ? 'Saving…' : editing ? 'Save' : 'Register class' }}
          </button>
        </div>
      </form>
    </AppModal>
  </Teleport>
</template>
