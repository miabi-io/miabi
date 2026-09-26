<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { stackApi } from '@/api/stacks'
import type { Stack } from '@/api/types'
import AppModal from '@/components/AppModal.vue'
import LocationPicker from '@/components/LocationPicker.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Stack[]>([])
const search = ref('')
// Client-side filter (the list isn't paginated) over the names shown in the
// table plus the description, so searching for what you can read works.
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return items.value
  return items.value.filter((s) =>
    s.name.toLowerCase().includes(q) ||
    (s.display_name || '').toLowerCase().includes(q) ||
    (s.description || '').toLowerCase().includes(q) ||
    (s.docker_name || '').toLowerCase().includes(q),
  )
})
const loading = ref(false)
const showCreate = ref(false)
const showImport = ref(false)
const saving = ref(false)
const importing = ref(false)
const form = ref({ name: '', description: '', location: '' })
const importForm = ref({ name: '', compose: '', location: '' })

function stackBadge(s: Stack) {
  const total = s.status?.total ?? 0
  const running = s.status?.running ?? 0
  if (total === 0) return 'badge-neutral'
  if (running === total) return 'badge-success'
  if (running === 0) return 'badge-danger'
  return 'badge-warning'
}

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await stackApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  form.value = { name: '', description: '', location: '' }
  showCreate.value = true
}

async function create() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    const stack = (await stackApi.create(currentWorkspaceId.value, {
      name: form.value.name.trim(),
      description: form.value.description.trim() || undefined,
      location: form.value.location || undefined,
    })).data.data
    notify.success(t('notify.stacks.created'))
    showCreate.value = false
    if (stack) router.push(`/stacks/${stack.id}`)
    else load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

function openImport() {
  importForm.value = { name: '', compose: '', location: '' }
  showImport.value = true
}

async function runImport() {
  if (!currentWorkspaceId.value) return
  importing.value = true
  try {
    const res = (await stackApi.import(currentWorkspaceId.value, importForm.value.name.trim(), importForm.value.compose, importForm.value.location)).data.data
    const created = res?.created.length ?? 0
    const vols = res?.volumes.length ?? 0
    const reqs = res?.port_requests ?? 0
    const skipped = res?.skipped.length ?? 0
    const conflicts = res?.port_conflicts ?? []
    const parts = [t('notify.stacks.importedApps', created)]
    if (vols) parts.push(t('count.volumes', vols))
    if (reqs) parts.push(t('notify.stacks.importedPortRequests', reqs))
    if (skipped) parts.push(t('notify.stacks.importedSkipped', skipped))
    notify.success(parts.join(', '))
    // Conflicting host ports were filed pending (not published) so the stack
    // still imports — tell the user which ports clashed and with what.
    if (conflicts.length) {
      const lines = conflicts.map((c) => `${c.host_port}/${c.protocol} (in use by ${c.used_by})`).join(', ')
      notify.error(t('notify.stacks.theseHostPortsAreAlready', { lines: lines }), 'Port conflicts')
    }
    showImport.value = false
    if (res?.stack) router.push(`/stacks/${res.stack.id}`)
    else load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e, 'Failed to import compose')
  } finally {
    importing.value = false
  }
}

</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('stacks.stacks') }}</h1>
        <p class="subtitle">Group related applications for {{ ws.contextLabel }}.</p>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="ws.canEdit" class="btn btn-secondary" @click="openImport">
          <span class="mdi mdi-import"></span>{{ $t('stacks.importCompose') }}</button>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
          <span class="mdi mdi-plus"></span>{{ $t('stacks.newStack') }}</button>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-layers-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('stacks.noStacks') }}</h3>
        <p>{{ $t('stacks.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('stacks.createAStack') }}</button>
      </div>
      <template v-else>
        <div class="card-body toolbar">
          <div class="search">
            <span class="mdi mdi-magnify"></span>
            <input
              v-model="search"
              class="form-input"
              type="search"
              :aria-label="$t('stacks.searchStacks')"
              :placeholder="$t('stacks.searchPlaceholder')"
            />
          </div>
          <span class="text-muted text-sm">{{ filtered.length }} of {{ items.length }}</span>
        </div>

        <div v-if="filtered.length === 0" class="empty-state" style="padding: 40px">
          <span class="mdi mdi-magnify" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No stacks match “{{ search }}”.</p>
        </div>

        <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('stacks.stack') }}</th><th>{{ $t('db.dockerName') }}</th><th>{{ $t('stacks.apps') }}</th><th>{{ $t('dashboard.col.status') }}</th></tr></thead>
          <tbody>
            <tr v-for="s in filtered" :key="s.id" class="row-clickable" :title="s.description"
              @click="router.push(`/stacks/${s.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-layers-outline" style="font-size: 14px"></span></span>
                  <!-- Label above, permanent handle below — the same shape the
                       applications list uses, so the two read alike. -->
                  <span class="cell-text">
                    <span class="cell-title">{{ s.display_name || s.name }}</span>
                    <span class="cell-sub">{{ s.name }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ s.docker_name }}</td>
              <td class="cell-sub">{{ s.app_count ?? 0 }}</td>
              <td>
                <span class="badge badge-dot" :class="stackBadge(s)">
                  {{ s.status?.running ?? 0 }}/{{ s.status?.total ?? 0 }} running
                </span>
              </td>
            </tr>
          </tbody>
        </table>
        </div>
      </template>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>{{ $t('stacks.newStack') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" :placeholder="$t('db.eGBlog')" :aria-label="$t('apps.form.name')" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('plans.description') }}<span class="text-muted">{{ $t('stacks.optional') }}</span></label>
              <input v-model="form.description" class="form-input" placeholder="WordPress + MySQL + Redis" :aria-label="$t('plans.description')" />
            </div>
            <LocationPicker v-model="form.location" :allow-pin="false" />
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Creating…' : 'Create stack' }}</button>
          </div>
        </form>
      </AppModal>

      <AppModal v-if="showImport" max-width="640px" @close="showImport = false">
        <div class="modal-header">
          <h3>{{ $t('stacks.importFromDockerCompose') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showImport = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="runImport">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('stacks.stackName') }}</label>
              <input v-model="importForm.name" class="form-input" :placeholder="$t('db.eGBlog')" :aria-label="$t('stacks.stackName')" required autofocus />
            </div>
            <LocationPicker v-model="importForm.location" :allow-pin="false" />
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">docker-compose.yml</label>
              <textarea v-model="importForm.compose" class="form-input" rows="12" spellcheck="false" style="font-family: monospace; font-size: 12px" placeholder="services:&#10;  web:&#10;    image: nginx:1.25&#10;    ports:&#10;      - 80&#10;    environment:&#10;      FOO: bar" :aria-label="$t('stacks.dockerComposeYml')" required></textarea>
              <p class="form-hint">{{ $t('stacks.importHint') }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showImport = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="importing">{{ importing ? 'Importing…' : 'Import stack' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.text-muted { color: var(--text-muted); }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.search { position: relative; flex: 1; max-width: 360px; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; }
</style>
