<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { copyText } from '@/utils/clipboard'
import type { Runner, RunnerAdapter } from '@/api/runners'

const props = defineProps<{
  adapter: RunnerAdapter
  canEdit: boolean
  title: string
  subtitle: string
  // shared = the platform pool (admin); tweaks copy only.
  shared?: boolean
  // detailRouteName links each runner's name to its detail page (scope-specific
  // route: workspace vs admin). When unset the name renders as plain text.
  detailRouteName?: string
  // createLimit caps how many runners may be registered (-1 or omitted = no cap).
  // When the current count reaches it, the register action is disabled and
  // limitNote (if given) explains why.
  createLimit?: number
  limitNote?: string
}>()

const notify = useNotificationStore()

const runners = ref<Runner[]>([])
// atCreateLimit: the pool has reached its allowed size, so registering is blocked
// (the backend enforces this too). Omitted / negative createLimit = no cap.
const atCreateLimit = computed(
  () => props.createLimit !== undefined && props.createLimit >= 0 && runners.value.length >= props.createLimit,
)
const loading = ref(false)
const saving = ref(false)
const showRegister = ref(false)
const createdToken = ref<string | null>(null)

// Register form.
const name = ref('')
const displayName = ref('')
const labelsText = ref('')
const concurrency = ref(1)

// Default image for the preview; replaced by the server-configured image
// (MIABI_RUNNER_IMAGE) returned in the create response once a runner is registered.
const image = ref('miabi/runner:latest')
const controlUrl = computed(() => window.location.origin)
const runCommand = computed(
  () =>
    `docker run -d --name miabi-runner --restart unless-stopped \\\n` +
    `  -e MIABI_CONTROL_URL=${controlUrl.value} \\\n` +
    `  -e MIABI_RUNNER_TOKEN=${createdToken.value ?? '<token>'} \\\n` +
    `  -v /var/run/docker.sock:/var/run/docker.sock \\\n` +
    `  ${image.value}`,
)

async function load() {
  loading.value = true
  try {
    runners.value = await props.adapter.list()
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function openRegister() {
  name.value = ''
  displayName.value = ''
  labelsText.value = ''
  concurrency.value = 1
  createdToken.value = null
  showRegister.value = true
}

async function register() {
  saving.value = true
  try {
    const labels = labelsText.value
      .split(',')
      .map((l) => l.trim())
      .filter(Boolean)
    const res = await props.adapter.create({
      name: name.value.trim(),
      display_name: displayName.value.trim() || undefined,
      labels: labels.length ? labels : undefined,
      concurrency: concurrency.value,
    })
    createdToken.value = res.token
    if (res.image) image.value = res.image
    notify.success('Runner registered — copy the token now')
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function toggleCordon(r: Runner) {
  try {
    const updated = await props.adapter.cordon(r.id, !r.cordoned)
    const i = runners.value.findIndex((x) => x.id === r.id)
    if (i >= 0) runners.value[i] = updated
    notify.success(r.cordoned ? 'Runner resumed' : 'Runner cordoned')
  } catch (e) {
    notify.apiError(e)
  }
}

const pendingRegenerate = ref<Runner | null>(null)
const regenerating = ref(false)

async function confirmRegenerate() {
  const r = pendingRegenerate.value
  if (!r) return
  regenerating.value = true
  try {
    createdToken.value = await props.adapter.regenerateToken(r.id)
    name.value = r.name
    showRegister.value = true
    pendingRegenerate.value = null
    notify.success('New token issued — copy it now')
  } catch (e) {
    notify.apiError(e)
  } finally {
    regenerating.value = false
  }
}

const pendingDelete = ref<Runner | null>(null)
const deleting = ref(false)

async function confirmRemove() {
  const r = pendingDelete.value
  if (!r) return
  deleting.value = true
  try {
    await props.adapter.remove(r.id)
    notify.success('Runner deleted')
    pendingDelete.value = null
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}

function statusBadge(r: Runner): { text: string; cls: string } {
  if (r.cordoned) return { text: 'cordoned', cls: 'badge-warning' }
  if (r.connected || r.status === 'online') return { text: 'online', cls: 'badge-success badge-dot' }
  if (r.status === 'draining') return { text: 'draining', cls: 'badge-warning' }
  return { text: 'offline', cls: 'badge-danger' }
}

function platform(r: Runner): string {
  return [r.os, r.arch].filter(Boolean).join('/') || '—'
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ title }}</h1>
        <p class="subtitle">{{ subtitle }}</p>
      </div>
      <button
        v-if="canEdit"
        class="btn btn-primary"
        :disabled="atCreateLimit"
        :title="atCreateLimit ? limitNote : ''"
        @click="openRegister"
      >
        <span class="mdi mdi-plus"></span> Add runner
      </button>
    </div>

    <div v-if="canEdit && atCreateLimit && limitNote" class="app-banner app-banner--info" style="margin-bottom: 16px">
      <span class="mdi mdi-information-outline app-banner-icon"></span>
      <div class="app-banner-content">
        <p class="app-banner-text">{{ limitNote }}</p>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && runners.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="runners.length === 0" class="empty-state">
        <span class="mdi mdi-cog-transfer-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No runners</h3>
        <p v-if="canEdit">Register a build machine so builds and pipelines run here instead of on your app nodes.</p>
        <p v-else-if="shared">No platform-shared runners are available yet.</p>
        <p v-else>No runners have been registered.</p>
        <button v-if="canEdit" class="btn btn-primary mt-4" :disabled="atCreateLimit" :title="atCreateLimit ? limitNote : ''" @click="openRegister">Add a runner</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Labels</th>
              <th>Concurrency</th>
              <th>Platform</th>
              <th>Version</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in runners" :key="r.id">
              <td>
                <router-link v-if="detailRouteName" :to="{ name: detailRouteName, params: { id: r.id } }" class="cell-title link">{{ r.display_name || r.name }}</router-link>
                <span v-else class="cell-title">{{ r.display_name || r.name }}</span>
                <span class="cell-sub">{{ r.name }}</span>
              </td>
              <td><span class="badge" :class="statusBadge(r).cls">{{ statusBadge(r).text }}</span></td>
              <td>
                <span v-if="!r.labels || r.labels.length === 0" class="cell-sub">—</span>
                <span v-for="l in r.labels || []" :key="l" class="badge badge-neutral" style="margin-right: 4px">{{ l }}</span>
              </td>
              <td class="cell-sub">{{ r.concurrency }}</td>
              <td class="cell-sub">{{ platform(r) }}</td>
              <td class="cell-sub">{{ r.version || '—' }}</td>
              <td style="text-align: right; white-space: nowrap">
                <template v-if="canEdit">
                  <button class="btn btn-secondary btn-sm" @click="toggleCordon(r)">{{ r.cordoned ? 'Resume' : 'Cordon' }}</button>
                  <button class="btn btn-secondary btn-sm" style="margin-left: 8px" @click="pendingRegenerate = r">Token</button>
                  <button class="btn btn-danger btn-sm" style="margin-left: 8px" @click="pendingDelete = r">Delete</button>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="showRegister" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ shared ? 'Add shared runner' : 'Add runner' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showRegister = false"><span class="mdi mdi-close"></span></button>
          </div>

          <form v-if="!createdToken" @submit.prevent="register">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="name" class="form-input" placeholder="e.g. amd64-builder" required autofocus aria-label="Name" />
              </div>
              <div class="form-group">
                <label class="form-label">Display name <span class="text-muted">(optional)</span></label>
                <input v-model="displayName" class="form-input" placeholder="e.g. Prod build box" aria-label="Display name" />
              </div>
              <div class="form-group">
                <label class="form-label">Labels <span class="text-muted">(comma-separated)</span></label>
                <input v-model="labelsText" class="form-input" placeholder="arch=amd64, buildkit, gpu" style="font-family: monospace" aria-label="Labels" />
                <p class="form-hint">A job runs on this runner only when its required labels are all present.</p>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Concurrency</label>
                <input v-model.number="concurrency" type="number" min="1" class="form-input" style="max-width: 120px" aria-label="Concurrency" />
                <p class="form-hint">How many jobs this runner may run at once.</p>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showRegister = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Add runner' }}</button>
            </div>
          </form>

          <template v-else>
            <div class="modal-body">
              <div class="app-banner app-banner--warning">
                <span class="mdi mdi-alert-outline app-banner-icon"></span>
                <div class="app-banner-content">
                  <p class="app-banner-title">Copy the registration token now</p>
                  <p class="app-banner-text">This is the only time it is shown. Run the runner on your build machine:</p>
                </div>
              </div>
              <div class="code-block" style="margin-top: 14px; white-space: pre">{{ runCommand }}</div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="copy(createdToken!)">Copy token</button>
              <button type="button" class="btn btn-secondary" @click="copy(runCommand)">Copy command</button>
              <button type="button" class="btn btn-primary" @click="showRegister = false">Done</button>
            </div>
          </template>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingRegenerate"
      title="Regenerate token"
      :message="pendingRegenerate ? `Regenerate ${pendingRegenerate.name}'s token? Its running container must be restarted with the new token.` : ''"
      confirm-label="Regenerate"
      variant="danger"
      :busy="regenerating"
      @confirm="confirmRegenerate"
      @cancel="pendingRegenerate = null"
    />

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete runner"
      :message="pendingDelete ? `Delete runner “${pendingDelete.name}”? Its container will no longer be able to connect, and any queued builds fall back to another runner.` : ''"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmRemove"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
a.cell-title.link { color: inherit; text-decoration: none; cursor: pointer; }
a.cell-title.link:hover { color: var(--primary-500); text-decoration: underline; }
</style>
