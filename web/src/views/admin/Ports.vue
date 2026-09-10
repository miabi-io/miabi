<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { portBindingApi } from '@/api/portBindings'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { PortEntry, PortNodeOverview, PortOverview } from '@/api/types'

// Host ports are a node-wide shared resource, so publishing one needs an admin's
// approval. This is where that decision is made — and where the ports already
// open on each node are visible, which the binding table alone cannot show.
const notify = useNotificationStore()

const overview = ref<PortOverview | null>(null)
const loading = ref(false)
const acting = ref<number | null>(null)

const rejecting = ref<PortEntry | null>(null)
const rejectNote = ref('')

async function load() {
  loading.value = true
  try {
    overview.value = (await portBindingApi.overview()).data.data ?? null
  } catch (e) {
    notify.apiError(e, 'Failed to load ports')
  } finally {
    loading.value = false
  }
}
onMounted(load)

const pending = computed<{ node: PortNodeOverview; entry: PortEntry }[]>(() =>
  (overview.value?.nodes ?? []).flatMap((node) =>
    node.entries.filter((e) => e.state === 'pending').map((entry) => ({ node, entry })),
  ),
)

const unmanaged = computed(() =>
  (overview.value?.nodes ?? []).reduce((n, node) => n + node.entries.filter((e) => e.state === 'unmanaged').length, 0),
)

const range = computed(() =>
  overview.value ? `${overview.value.min_port}–${overview.value.max_port}` : '',
)

function inRange(port: number): boolean {
  if (!overview.value) return true
  return port >= overview.value.min_port && port <= overview.value.max_port
}

// A pending request whose port is already held cannot be approved — the service
// refuses it. Saying so before the click beats an error afterwards.
function blockedBy(node: PortNodeOverview, entry: PortEntry): string {
  if (entry.container) return entry.container
  const clash = node.entries.find(
    (e) => e !== entry && e.host_port === entry.host_port && e.protocol === entry.protocol && e.state !== 'pending',
  )
  return clash ? clash.app_name || clash.container || 'another binding' : ''
}

async function approve(entry: PortEntry) {
  if (!entry.binding_id) return
  acting.value = entry.binding_id
  try {
    await portBindingApi.approve(entry.binding_id)
    notify.success(`Host port ${entry.host_port}/${entry.protocol} approved — it publishes on the app's next deploy`)
    await load()
  } catch (e) {
    notify.apiError(e, 'Failed to approve')
  } finally {
    acting.value = null
  }
}

function openReject(entry: PortEntry) {
  rejecting.value = entry
  rejectNote.value = ''
}

async function confirmReject() {
  const entry = rejecting.value
  if (!entry?.binding_id) return
  acting.value = entry.binding_id
  try {
    await portBindingApi.reject(entry.binding_id, rejectNote.value.trim())
    notify.success('Request rejected')
    rejecting.value = null
    await load()
  } catch (e) {
    notify.apiError(e, 'Failed to reject')
  } finally {
    acting.value = null
  }
}

const STATE_LABEL: Record<string, string> = {
  pending: 'Pending review',
  reserved: 'Approved · not published',
  published: 'Published',
  unmanaged: 'Not managed by Miabi',
}
const STATE_CLASS: Record<string, string> = {
  pending: 'badge-warning',
  reserved: 'badge-info',
  published: 'badge-success',
  unmanaged: 'badge-neutral',
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Ports</h1>
      <button class="btn btn-secondary" :disabled="loading" @click="load">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span> Refresh
      </button>
    </div>

    <!-- Review queue first: it is the only part of this page with a deadline. -->
    <div class="card" style="margin-bottom: 16px">
      <div class="card-body">
        <div class="section-head">
          <h2 class="card-title">
            Awaiting review
            <span v-if="pending.length" class="badge badge-warning">{{ pending.length }}</span>
          </h2>
          <span v-if="range" class="text-muted approvable">Approvable range {{ range }}</span>
        </div>

        <p v-if="!pending.length" class="text-muted empty-line">
          No requests waiting. A developer asking to publish a host port appears here.
        </p>

        <table v-else class="table">
          <thead>
            <tr>
              <th>Host port</th>
              <th>Application</th>
              <th>Node</th>
              <th>Requested</th>
              <th class="right">Decision</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="{ node, entry } in pending" :key="entry.binding_id">
              <td>
                <span class="mono">{{ entry.host_port }}/{{ entry.protocol }}</span>
                <div v-if="!inRange(entry.host_port)" class="cell-sub text-warning">
                  Outside the approvable range {{ range }}
                </div>
                <div v-else-if="blockedBy(node, entry)" class="cell-sub text-danger">
                  Already held by {{ blockedBy(node, entry) }}
                </div>
              </td>
              <td>
                <span>{{ entry.app_name || `application ${entry.application_id}` }}</span>
                <div class="cell-sub text-muted">container port {{ entry.container_port }}</div>
              </td>
              <td>{{ node.name || `node ${node.server_id}` }}</td>
              <td class="text-muted">{{ entry.created_at ? new Date(entry.created_at).toLocaleString() : '' }}</td>
              <td class="right">
                <button class="btn btn-sm btn-secondary" :disabled="acting === entry.binding_id"
                  @click="openReject(entry)">
                  Reject
                </button>
                <button class="btn btn-sm btn-primary" style="margin-left: 8px" :disabled="acting === entry.binding_id"
                  @click="approve(entry)">
                  Approve
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- The map: what each node actually has open, which is not the same as what
         the binding table says. -->
    <div v-for="node in overview?.nodes ?? []" :key="node.server_id" class="card" style="margin-bottom: 16px">
      <div class="card-body">
        <div class="section-head">
          <h2 class="card-title">{{ node.name || `Node ${node.server_id}` }}</h2>
          <span v-if="!node.inspected" class="badge badge-neutral" title="Docker could not be reached on this node">
            not inspected
          </span>
        </div>

        <p v-if="!node.inspected" class="text-muted empty-line">
          This node could not be reached, so these rows come from Miabi's records alone. Ports opened by
          containers Miabi does not manage are not shown, and an approved port cannot be confirmed as live.
        </p>

        <p v-if="!node.entries.length" class="text-muted empty-line">No external ports.</p>

        <table v-else class="table">
          <thead>
            <tr>
              <th>Host port</th>
              <th>State</th>
              <th>Used by</th>
              <th>Container</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in node.entries" :key="`${e.host_port}/${e.protocol}`">
              <td class="mono">{{ e.host_port }}/{{ e.protocol }}</td>
              <td>
                <span class="badge" :class="STATE_CLASS[e.state]">{{ STATE_LABEL[e.state] }}</span>
                <span v-if="e.managed" class="badge badge-neutral" style="margin-left: 6px"
                  title="Created by Miabi for route ingress; not a user request">auto</span>
              </td>
              <td>
                <template v-if="e.state === 'unmanaged'">
                  <span class="text-muted">Not a Miabi application</span>
                </template>
                <template v-else>
                  {{ e.app_name || `application ${e.application_id}` }}
                  <span v-if="e.container_port" class="cell-sub text-muted">→ {{ e.container_port }}</span>
                </template>
              </td>
              <td class="mono text-muted">{{ e.container || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <p v-if="unmanaged" class="text-muted footnote">
      {{ unmanaged }} port{{ unmanaged === 1 ? '' : 's' }} held by containers Miabi does not manage. They are
      not errors — an imported container or the gateway itself will appear here — but they are what a new
      request on the same port will collide with.
    </p>

    <ConfirmDialog :open="!!rejecting" title="Reject this request?"
      :message="`Host port ${rejecting?.host_port}/${rejecting?.protocol} will not be published. The requester sees your note.`"
      confirm-label="Reject" variant="danger" :busy="acting !== null" @cancel="rejecting = null"
      @confirm="confirmReject">
      <label class="form-label" for="reject-note">Reason (optional)</label>
      <input id="reject-note" v-model="rejectNote" class="form-input"
        placeholder="e.g. use a route and a domain instead" />
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.card-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
}

.approvable {
  font-size: 12px;
}

.empty-line {
  margin: 0;
  font-size: 14px;
}

.cell-sub {
  font-size: 12px;
  margin-top: 2px;
}

.right {
  text-align: right;
}

.footnote {
  font-size: 13px;
  margin: 0 0 24px;
}
</style>
