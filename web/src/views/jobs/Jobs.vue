<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { jobApi, usageApi, type CronJobInput } from '@/api/resources'
import { appApi } from '@/api/apps'
import { registryApi } from '@/api/registries'
import type { Job, CronJob, Application, Registry } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const tab = ref<'runs' | 'scheduled'>('runs')
const jobs = ref<Job[]>([])
const cronJobs = ref<CronJob[]>([])
const apps = ref<Application[]>([])
const registries = ref<Registry[]>([])
let poll: ReturnType<typeof setInterval> | null = null

const hasActive = computed(() => jobs.value.some((j) => j.status === 'pending' || j.status === 'running'))

// Whether this workspace's containers must drop root. Mirrors the server rule so the
// run-as field explains itself instead of failing the run with a 400.
const requireNonRoot = ref(false)
function runAsUserError(v: string): string {
  v = v.trim()
  if (!v) return ''
  if (!/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,31})?$/.test(v)) {
    return 'Use "uid", "uid:gid", "name" or "name:group".'
  }
  if (requireNonRoot.value && !/^[1-9][0-9]*(:[0-9]+)?$/.test(v)) {
    return 'This workspace requires a non-root numeric uid (e.g. 1000).'
  }
  return ''
}
const runUserError = computed(() => runAsUserError(runForm.value.run_as_user))
const cronUserError = computed(() => runAsUserError(cronForm.value.run_as_user))

async function load() {
  const id = currentWorkspaceId.value
  if (!id) { jobs.value = []; cronJobs.value = []; return }
  try {
    const [j, c, a, r] = await Promise.all([
      jobApi.list(id),
      jobApi.cronJobs(id),
      appApi.list(id),
      registryApi.list(id),
    ])
    jobs.value = j.data.data ?? []
    cronJobs.value = c.data.data ?? []
    apps.value = a.data.data ?? []
    registries.value = r.data.data ?? []
    // Separate + best-effort: a usage error must not blank the jobs list.
    try { requireNonRoot.value = (await usageApi.get(id)).data.data?.capabilities?.require_non_root === true } catch { requireNonRoot.value = false }
    if (logModal.value) {
      const fresh = jobs.value.find((x) => x.id === logModal.value?.id)
      if (fresh) logModal.value = fresh
    }
  } catch (e) { notify.apiError(e) }
}

watch(currentWorkspaceId, load, { immediate: true })
poll = setInterval(() => { if (hasActive.value || logModal.value) load() }, 4000)
onBeforeUnmount(() => { if (poll) clearInterval(poll) })

function splitCommand(s: string): string[] {
  return s.trim().split(/\s+/).filter(Boolean)
}
function appName(id: number) {
  return apps.value.find((a) => a.id === id)?.name || `app #${id}`
}

// --- Run a one-off job ---
const showRun = ref(false)
const running = ref(false)
const runForm = ref<{ app: number | null; name: string; command: string; image: string; registry: number | null; run_as_user: string; timeout: number }>({ app: null, name: '', command: '', image: '', registry: null, run_as_user: '', timeout: 0 })
function openRun() {
  runForm.value = { app: apps.value[0]?.id ?? null, name: '', command: '', image: '', registry: null, run_as_user: '', timeout: 0 }
  showRun.value = true
}
async function run() {
  const id = currentWorkspaceId.value
  if (!id) return
  if (!runForm.value.app) { notify.error('Select an application'); return }
  const command = splitCommand(runForm.value.command)
  if (command.length === 0) { notify.error('A command is required'); return }
  running.value = true
  try {
    const image = runForm.value.image.trim()
    await jobApi.run(id, {
      application_id: runForm.value.app,
      name: runForm.value.name.trim() || undefined,
      command,
      image: image || undefined,
      registry_id: image ? runForm.value.registry : undefined,
      run_as_user: runForm.value.run_as_user.trim() || undefined,
      timeout_secs: runForm.value.timeout > 0 ? runForm.value.timeout : undefined,
    })
    notify.success('Job started')
    showRun.value = false
    tab.value = 'runs'
    load()
  } catch (e) { notify.apiError(e) }
  finally { running.value = false }
}

const logModal = ref<Job | null>(null)
function viewLogs(j: Job) { logModal.value = j }
async function cancelJob(j: Job) {
  const id = currentWorkspaceId.value
  if (!id) return
  try { await jobApi.cancel(id, j.id); notify.success('Job canceled'); load() }
  catch (e) { notify.apiError(e) }
}
async function deleteJob(j: Job) {
  const id = currentWorkspaceId.value
  if (!id) return
  try { await jobApi.remove(id, j.id); load() }
  catch (e) { notify.apiError(e) }
}

// --- CronJobs ---
const showCron = ref(false)
const savingCron = ref(false)
const editingCronId = ref<number | null>(null)
const cronForm = ref<{ app: number | null; name: string; schedule: string; image: string; registry: number | null; run_as_user: string; concurrency_policy: 'allow' | 'forbid' | 'replace'; enabled: boolean; timeout_secs: number; history_limit: number }>(
  { app: null, name: '', schedule: '0 3 * * *', image: '', registry: null, run_as_user: '', concurrency_policy: 'allow', enabled: true, timeout_secs: 0, history_limit: 0 },
)
const cronCommandStr = ref('')

function openCreateCron() {
  editingCronId.value = null
  cronForm.value = { app: apps.value[0]?.id ?? null, name: '', schedule: '0 3 * * *', image: '', registry: null, run_as_user: '', concurrency_policy: 'allow', enabled: true, timeout_secs: 0, history_limit: 0 }
  cronCommandStr.value = ''
  showCron.value = true
}
function openEditCron(c: CronJob) {
  editingCronId.value = c.id
  cronForm.value = { app: c.application_id, name: c.name, schedule: c.schedule, image: c.image || '', registry: c.registry_id ?? null, run_as_user: c.run_as_user || '', concurrency_policy: c.concurrency_policy, enabled: c.enabled, timeout_secs: c.timeout_secs, history_limit: c.history_limit }
  cronCommandStr.value = (c.command || []).join(' ')
  showCron.value = true
}
async function saveCron() {
  const id = currentWorkspaceId.value
  if (!id) return
  if (!cronForm.value.app) { notify.error('Select an application'); return }
  const command = splitCommand(cronCommandStr.value)
  if (command.length === 0) { notify.error('A command is required'); return }
  if (!cronForm.value.schedule.trim()) { notify.error('A schedule is required'); return }
  savingCron.value = true
  const image = cronForm.value.image.trim()
  const input: CronJobInput = {
    application_id: cronForm.value.app,
    name: cronForm.value.name, schedule: cronForm.value.schedule, command,
    image: image || undefined,
    registry_id: image ? cronForm.value.registry : null,
    run_as_user: cronForm.value.run_as_user.trim(),
    concurrency_policy: cronForm.value.concurrency_policy, enabled: cronForm.value.enabled,
    timeout_secs: cronForm.value.timeout_secs, history_limit: cronForm.value.history_limit,
  }
  try {
    if (editingCronId.value) await jobApi.updateCronJob(id, editingCronId.value, input)
    else await jobApi.createCronJob(id, input)
    notify.success(editingCronId.value ? 'CronJob updated' : 'CronJob created')
    showCron.value = false
    load()
  } catch (e) { notify.apiError(e) }
  finally { savingCron.value = false }
}
async function runCronNow(c: CronJob) {
  const id = currentWorkspaceId.value
  if (!id) return
  try { await jobApi.runCronJobNow(id, c.id); notify.success('Run started'); tab.value = 'runs'; load() }
  catch (e) { notify.apiError(e) }
}

// --- Confirm dialog ---
const confirm = ref<{ title: string; message: string; run: () => Promise<void> } | null>(null)
const confirmBusy = ref(false)
function askDeleteJob(j: Job) {
  confirm.value = { title: 'Delete job run', message: `Delete job run #${j.id}? This cannot be undone.`, run: () => deleteJob(j) }
}
function askDeleteCron(c: CronJob) {
  confirm.value = { title: 'Delete cronjob', message: `Delete cronjob "${c.name || c.schedule}"? Scheduled runs will stop.`, run: () => delCron(c) }
}
async function delCron(c: CronJob) {
  const id = currentWorkspaceId.value
  if (!id) return
  try { await jobApi.deleteCronJob(id, c.id); notify.success('CronJob deleted'); load() }
  catch (e) { notify.apiError(e) }
}
async function runConfirm() {
  if (!confirm.value) return
  confirmBusy.value = true
  try { await confirm.value.run(); confirm.value = null }
  finally { confirmBusy.value = false }
}

function cmd(c: string[]) { return (c || []).join(' ') }
function badge(s: string) {
  return s === 'succeeded' ? 'badge-success' : s === 'failed' ? 'badge-danger' : s === 'canceled' ? 'badge-warning' : 'badge-info'
}
function when(iso?: string) { return iso ? new Date(iso).toLocaleString() : '—' }
const noApps = computed(() => apps.value.length === 0)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Jobs</h1>
        <div class="text-muted text-sm">Run one-off commands and schedules in your apps' runtime environments.</div>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="ws.canEdit && tab === 'runs'" class="btn btn-primary" :disabled="noApps" @click="openRun"><span class="mdi mdi-play"></span> Run job</button>
        <button v-if="ws.canEdit && tab === 'scheduled'" class="btn btn-primary" :disabled="noApps" @click="openCreateCron"><span class="mdi mdi-plus"></span> New cronjob</button>
      </div>
    </div>

    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'runs' }" @click="tab = 'runs'">Runs</button>
      <button class="tab" :class="{ active: tab === 'scheduled' }" @click="tab = 'scheduled'">Scheduled</button>
    </div>

    <!-- Runs -->
    <div v-if="tab === 'runs'" class="card">
      <div v-if="jobs.length === 0" class="empty-state">
        <span class="mdi mdi-console-line" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No job runs yet</h3>
        <p>Run a one-off command in an app's environment — migrations, backfills, scripts.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" :disabled="noApps" @click="openRun">Run a job</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>App</th><th>Command</th><th>Status</th><th>Exit</th><th>Started</th><th></th></tr></thead>
          <tbody>
            <tr v-for="j in jobs" :key="j.id">
              <td class="cell-sub">{{ j.app_name || appName(j.application_id) }}</td>
              <td>
                <span class="cell-title" style="font-family: monospace">{{ j.name || cmd(j.command) }}</span>
                <div v-if="j.name" class="cell-sub" style="font-family: monospace">{{ cmd(j.command) }}</div>
                <div v-if="j.source === 'scheduled'" class="cell-sub"><span class="mdi mdi-clock-outline"></span> scheduled</div>
              </td>
              <td><span class="badge badge-dot" :class="badge(j.status)">{{ j.status }}</span></td>
              <td class="cell-sub">{{ j.exit_code ?? '—' }}</td>
              <td class="cell-sub">{{ when(j.started_at) }}</td>
              <td class="text-right table-actions">
                <button class="btn-icon btn-icon-muted" title="Logs" aria-label="Logs" @click="viewLogs(j)"><span class="mdi mdi-text-box-outline"></span></button>
                <button v-if="ws.canEdit && (j.status === 'running' || j.status === 'pending')" class="btn btn-sm btn-secondary" @click="cancelJob(j)">Cancel</button>
                <button v-else-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="askDeleteJob(j)"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Scheduled -->
    <div v-else class="card">
      <div v-if="cronJobs.length === 0" class="empty-state">
        <span class="mdi mdi-calendar-clock" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No scheduled jobs</h3>
        <p>A CronJob spawns a job on a schedule (UTC). Missed runs while down are not backfilled.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" :disabled="noApps" @click="openCreateCron">New cronjob</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>App</th><th>Name</th><th>Schedule</th><th>Command</th><th>Last run</th><th>State</th><th></th></tr></thead>
          <tbody>
            <tr v-for="c in cronJobs" :key="c.id">
              <td class="cell-sub">{{ c.app_name || appName(c.application_id) }}</td>
              <td class="cell-title">{{ c.name || '—' }}</td>
              <td class="cell-sub" style="font-family: monospace">{{ c.schedule }}</td>
              <td class="cell-sub" style="font-family: monospace">{{ cmd(c.command) }}</td>
              <td class="cell-sub">{{ when(c.last_run_at) }}</td>
              <td><span class="badge badge-dot" :class="c.enabled ? 'badge-success' : 'badge-warning'">{{ c.enabled ? 'enabled' : 'disabled' }}</span></td>
              <td class="text-right table-actions">
                <button v-if="ws.canEdit" class="btn btn-sm btn-secondary" title="Run now" @click="runCronNow(c)">Run now</button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEditCron(c)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="askDeleteCron(c)"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <!-- Run job modal -->
      <div v-if="showRun" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>Run a job</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showRun = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="run">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Application</label>
                <select v-model="runForm.app" class="form-select" required aria-label="Application">
                  <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option>
                </select>
                <p class="form-hint">Runs in this app's current image and environment.</p>
              </div>
              <div class="form-group">
                <label class="form-label">Command</label>
                <input v-model="runForm.command" class="form-input" placeholder="rails db:migrate" required autofocus style="font-family: monospace" aria-label="Command" />
                <p class="form-hint">Split on spaces (no shell quoting).</p>
              </div>
              <div class="form-group">
                <label class="form-label">Image <span class="text-muted">(optional)</span></label>
                <input v-model="runForm.image" class="form-input" placeholder="leave blank to use the app's current image" style="font-family: monospace" aria-label="Image" />
                <p class="form-hint">Run a different image in this app's environment (env, networks, node).</p>
              </div>
              <div v-if="runForm.image.trim()" class="form-group">
                <label class="form-label">Registry <span class="text-muted">(for private images)</span></label>
                <select v-model="runForm.registry" class="form-select" aria-label="Registry">
                  <option :value="null">App's registry / public</option>
                  <option v-for="r in registries" :key="r.id" :value="r.id">{{ r.name }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">Name <span class="text-muted">(optional)</span></label>
                <input v-model="runForm.name" class="form-input" placeholder="migrate" aria-label="Name" />
              </div>
              <div class="form-group">
                <label class="form-label">Run as user <span class="text-muted">(optional)</span></label>
                <input
                  v-model="runForm.run_as_user" class="form-input" style="font-family: monospace"
                  :placeholder="requireNonRoot ? '1000:1000' : 'leave blank to inherit the app\u2019s user'" aria-label="Run as user"
                />
                <p v-if="runUserError" class="form-hint" style="color: var(--danger)">{{ runUserError }}</p>
                <p v-else class="form-hint">
                  The account this run uses, like <code>docker run --user</code>. Blank inherits the app's.
                  <span v-if="requireNonRoot">This workspace requires a non-root numeric uid.</span>
                </p>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Timeout (seconds, 0 = default)</label>
                <input v-model.number="runForm.timeout" type="number" min="0" class="form-input" style="max-width: 160px" aria-label="Timeout (seconds, 0 = default)" />
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showRun = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="running">{{ running ? 'Starting…' : 'Run' }}</button>
            </div>
          </form>
        </div>
      </div>

      <!-- CronJob modal -->
      <div v-if="showCron" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ editingCronId ? 'Edit cronjob' : 'New cronjob' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCron = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="saveCron">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Application</label>
                <select v-model="cronForm.app" class="form-select" required :disabled="!!editingCronId" aria-label="Application">
                  <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">Name <span class="text-muted">(optional)</span></label>
                <input v-model="cronForm.name" class="form-input" placeholder="nightly-cleanup" aria-label="Name" />
              </div>
              <div class="form-group">
                <label class="form-label">Schedule (cron, UTC)</label>
                <input v-model="cronForm.schedule" class="form-input" placeholder="0 3 * * *" required style="font-family: monospace; max-width: 220px" aria-label="Schedule (cron, UTC)" />
              </div>
              <div class="form-group">
                <label class="form-label">Command</label>
                <input v-model="cronCommandStr" class="form-input" placeholder="rake cleanup" required style="font-family: monospace" aria-label="Command" />
              </div>
              <div class="form-group">
                <label class="form-label">Image <span class="text-muted">(optional)</span></label>
                <input v-model="cronForm.image" class="form-input" placeholder="leave blank to use the app's current image" style="font-family: monospace" aria-label="Image" />
              </div>
              <div v-if="cronForm.image.trim()" class="form-group">
                <label class="form-label">Registry <span class="text-muted">(for private images)</span></label>
                <select v-model="cronForm.registry" class="form-select" aria-label="Registry">
                  <option :value="null">App's registry / public</option>
                  <option v-for="r in registries" :key="r.id" :value="r.id">{{ r.name }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">Run as user <span class="text-muted">(optional)</span></label>
                <input
                  v-model="cronForm.run_as_user" class="form-input" style="font-family: monospace"
                  :placeholder="requireNonRoot ? '1000:1000' : 'leave blank to inherit the app\u2019s user'" aria-label="Run as user"
                />
                <p v-if="cronUserError" class="form-hint" style="color: var(--danger)">{{ cronUserError }}</p>
                <p v-else class="form-hint">Applied to every run this schedule spawns. Blank inherits the app's when each run fires.</p>
              </div>
              <div class="flex items-center gap-3" style="flex-wrap: wrap">
                <label class="form-group" style="margin-bottom: 0">
                  <span class="form-label">Concurrency</span>
                  <select v-model="cronForm.concurrency_policy" class="form-select" style="max-width: 160px">
                    <option value="allow">Allow</option>
                    <option value="forbid">Forbid</option>
                    <option value="replace">Replace</option>
                  </select>
                </label>
                <label class="form-group" style="margin-bottom: 0">
                  <span class="form-label">Timeout (s)</span>
                  <input v-model.number="cronForm.timeout_secs" type="number" min="0" class="form-input" style="max-width: 120px" />
                </label>
                <label class="form-group" style="margin-bottom: 0">
                  <span class="form-label">Keep last</span>
                  <input v-model.number="cronForm.history_limit" type="number" min="0" class="form-input" style="max-width: 110px" />
                </label>
                <label class="checkbox-row" style="align-self: flex-end">
                  <input type="checkbox" v-model="cronForm.enabled" /> Enabled
                </label>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showCron = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="savingCron">{{ savingCron ? 'Saving…' : 'Save' }}</button>
            </div>
          </form>
        </div>
      </div>

      <!-- Logs modal -->
      <div v-if="logModal" class="modal-overlay">
        <div class="modal" style="max-width: 720px; width: 100%">
          <div class="modal-header">
            <h3>Job #{{ logModal.id }} · <span class="badge badge-dot" :class="badge(logModal.status)">{{ logModal.status }}</span></h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="logModal = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="text-muted text-sm" style="margin-bottom: 8px; font-family: monospace">{{ logModal.image }} · {{ cmd(logModal.command) }}</div>
            <pre class="log-view">{{ logModal.logs || (logModal.status === 'pending' ? 'Waiting to start…' : '(no output)') }}</pre>
            <p v-if="logModal.error" class="text-sm" style="color: var(--danger-600); margin-top: 8px">{{ logModal.error }}</p>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!confirm"
      :title="confirm?.title ?? ''"
      :message="confirm?.message ?? ''"
      confirm-label="Delete"
      variant="danger"
      :busy="confirmBusy"
      @confirm="runConfirm"
      @cancel="confirm = null"
    />
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.tabs { display: flex; gap: 4px; margin-bottom: 16px; border-bottom: 1px solid var(--border-primary); }
.tab {
  padding: 8px 16px; background: none; border: none; border-bottom: 2px solid transparent;
  color: var(--text-secondary); cursor: pointer; font-size: 14px; font-family: inherit;
}
.tab.active { color: var(--primary-600); border-bottom-color: var(--primary-500); font-weight: 500; }
.log-view {
  background: var(--bg-tertiary); border-radius: 6px; padding: 12px; max-height: 50vh; overflow: auto;
  font-family: monospace; font-size: 12px; white-space: pre-wrap; word-break: break-word; margin: 0;
}
.checkbox-row { display: flex; align-items: center; gap: 6px; font-size: 14px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
</style>
