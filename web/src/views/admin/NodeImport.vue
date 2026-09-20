<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { nodesApi, type ImportableResources, type ImportableContainer, type ImportItem, type ImportMode, type ImportItemResult } from '@/api/nodes'
import { adminApi } from '@/api/admin'
import type { AdminWorkspace } from '@/api/types'

const route = useRoute()
const { t } = useI18n()
const router = useRouter()
const notify = useNotificationStore()
const id = Number(route.params.id)

const loading = ref(false)
const importing = ref(false)
const resources = ref<ImportableResources | null>(null)
const workspaces = ref<AdminWorkspace[]>([])
const results = ref<ImportItemResult[] | null>(null)

// Selection state.
const selContainers = ref<Set<string>>(new Set())
const selVolumes = ref<Set<string>>(new Set())
const selNetworks = ref<Set<string>>(new Set())
const appNames = ref<Record<string, string>>({})
// Stack name per compose project ('' = ungrouped); each defaults to a stack of the same name.
const groupStack = ref<Record<string, string>>({})

// Target.
const workspaceId = ref<number | null>(null)
const mode = ref<ImportMode>('adopt')

onMounted(load)

async function load() {
  loading.value = true
  results.value = null
  selContainers.value = new Set()
  selVolumes.value = new Set()
  selNetworks.value = new Set()
  appNames.value = {}
  groupStack.value = {}
  try {
    const [imp, ws] = await Promise.all([nodesApi.importable(id), adminApi.listWorkspaces()])
    resources.value = imp.data.data ?? { containers: [], volumes: [], networks: [] }
    workspaces.value = ws.data.data ?? []
    if (!workspaceId.value && workspaces.value.length) workspaceId.value = workspaces.value[0].id
    for (const c of resources.value.containers) {
      appNames.value[c.id] = c.suggested_name
      const key = c.compose_project || ''
      if (!(key in groupStack.value)) groupStack.value[key] = c.compose_project || ''
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// Group containers by compose project ('' = ungrouped).
const composeGroups = computed(() => {
  const groups: Record<string, ImportableContainer[]> = {}
  for (const c of resources.value?.containers ?? []) {
    const key = c.compose_project || ''
    ;(groups[key] ||= []).push(c)
  }
  return Object.entries(groups).sort((a, b) => a[0].localeCompare(b[0]))
})

function toggleContainer(c: ImportableContainer) {
  if (c.already_imported) return
  const next = new Set(selContainers.value)
  if (next.has(c.id)) {
    next.delete(c.id)
  } else {
    next.add(c.id)
    // Auto-select the container's dependencies (its volumes and networks).
    const vols = new Set(selVolumes.value)
    for (const v of c.volumes ?? []) if (!volumeImported(v)) vols.add(v)
    selVolumes.value = vols
    const nets = new Set(selNetworks.value)
    for (const n of c.networks ?? []) if (!networkImported(n)) nets.add(n)
    selNetworks.value = nets
  }
  selContainers.value = next
}

function toggleSet(set: 'vol' | 'net', name: string, alreadyImported: boolean) {
  if (alreadyImported) return
  const ref_ = set === 'vol' ? selVolumes : selNetworks
  const next = new Set(ref_.value)
  next.has(name) ? next.delete(name) : next.add(name)
  ref_.value = next
}

function volumeImported(name: string) {
  return resources.value?.volumes.find((v) => v.name === name)?.already_imported ?? false
}
function networkImported(name: string) {
  return resources.value?.networks.find((n) => n.name === name)?.already_imported ?? false
}

const selectedCount = computed(() => selContainers.value.size + selVolumes.value.size + selNetworks.value.size)

const canImport = computed(() => !!workspaceId.value && selectedCount.value > 0 && !importing.value)

// Stack name a container attaches to (its compose project's stack, editable per group).
function stackFor(c: ImportableContainer): string {
  return (groupStack.value[c.compose_project || ''] ?? '').trim()
}

async function doImport() {
  if (!workspaceId.value) return
  const byId = new Map(resources.value?.containers.map((c) => [c.id, c]) ?? [])
  const items: ImportItem[] = []
  for (const n of selNetworks.value) items.push({ kind: 'network', ref: n })
  for (const v of selVolumes.value) items.push({ kind: 'volume', ref: v })
  for (const cid of selContainers.value) {
    const c = byId.get(cid)
    items.push({
      kind: 'container',
      ref: cid,
      app_name: appNames.value[cid]?.trim() || undefined,
      mode: mode.value,
      stack_name: c ? stackFor(c) || undefined : undefined,
    })
  }
  importing.value = true
  try {
    const res = await nodesApi.import(id, { workspace_id: workspaceId.value, items })
    results.value = res.data.data?.items ?? []
    const failed = results.value.filter((r) => r.status === 'failed').length
    const imported = results.value.filter((r) => r.status === 'imported').length
    if (failed) notify.error(t('notify.nodeImport.importedFailed', { imported: imported, failed: failed }))
    else notify.success(`Imported ${imported} resource${imported === 1 ? '' : 's'}`)
  } catch (e) {
    notify.apiError(e)
  } finally {
    importing.value = false
  }
}

function goToNode() { router.push(`/admin/nodes/${id}`) }

function fmtPorts(c: ImportableContainer): string {
  return (c.ports ?? []).map((p) => (p.host_port ? `${p.host_port}:` : '') + `${p.container_port}/${p.protocol}`).join(', ')
}
</script>

<template>
  <div class="node-import">
    <div class="page-header">
      <div>
        <router-link :to="`/admin/nodes/${id}`" class="back-link"><span class="mdi mdi-arrow-left"></span> Node</router-link>
        <h1>Import existing resources</h1>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="card-body"><span class="spinner"></span> Scanning node…</div></div>

    <!-- Results -->
    <template v-else-if="results">
      <div class="card mb-4">
        <div class="card-header"><h2>Import results</h2></div>
        <div class="table-wrapper">
          <table class="ctable">
            <thead><tr><th>Kind</th><th>Resource</th><th>Status</th><th>Detail</th></tr></thead>
            <tbody>
              <tr v-for="(r, i) in results" :key="i">
                <td>{{ r.kind }}</td>
                <td class="mono trunc" :title="r.ref">{{ r.ref }}</td>
                <td>
                  <span class="badge" :class="{ 'badge-success': r.status === 'imported', 'badge-info': r.status === 'skipped', 'badge-danger': r.status === 'failed' }">{{ r.status }}</span>
                </td>
                <td class="text-muted">{{ r.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <button class="btn btn-primary" @click="goToNode">Done</button>
        <button class="btn btn-secondary" @click="load">Import more</button>
      </div>
    </template>

    <!-- Selection -->
    <template v-else-if="resources">
      <p class="text-muted intro">
        Adopt containers, volumes and networks that already run on this node. By default Miabi
        <strong>tracks them in place</strong> with zero downtime; they become fully native on their next deploy.
      </p>

      <div v-if="!resources.containers.length && !resources.volumes.length && !resources.networks.length" class="card">
        <div class="empty-state" style="padding: 32px"><p class="text-muted">No unmanaged resources to import on this node.</p></div>
      </div>

      <template v-else>
        <!-- Target -->
        <div class="card mb-4">
          <div class="card-header"><h2>Target</h2></div>
          <div class="card-body target">
            <div class="form-group">
              <label class="form-label">Workspace</label>
              <select v-model.number="workspaceId" class="form-select">
                <option v-for="w in workspaces" :key="w.id" :value="w.id">{{ w.name }}</option>
              </select>
            </div>
            <div class="form-group">
              <label class="form-label">Mode</label>
              <select v-model="mode" class="form-select">
                <option value="adopt">Adopt in place (no downtime)</option>
                <option value="reconcile">Reconcile now (recreate)</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Containers grouped by compose project -->
        <div v-if="resources.containers.length" class="card mb-4">
          <div class="card-header"><h2>Containers <span class="text-muted" style="font-weight: 400">{{ resources.containers.length }}</span></h2></div>
          <div class="card-body">
            <div v-for="[project, list] in composeGroups" :key="project || '_'" class="compose-group">
              <div v-if="project" class="compose-label">
                <span class="mdi mdi-layers-outline"></span> compose: <code>{{ project }}</code>
                <span class="stack-arrow mdi mdi-arrow-right"></span>
                <label class="stack-inline">stack
                  <input v-model="groupStack[project]" class="form-input form-input-sm stack-name" :placeholder="project" />
                </label>
              </div>
              <div v-else-if="composeGroups.length > 1" class="compose-label">
                ungrouped
                <label class="stack-inline">stack <span class="text-muted">(optional)</span>
                  <input v-model="groupStack['']" class="form-input form-input-sm stack-name" placeholder="none" />
                </label>
              </div>
              <div v-for="c in list" :key="c.id" class="res-row" :class="{ disabled: c.already_imported }">
                <label class="res-main">
                  <input type="checkbox" :checked="selContainers.has(c.id)" :disabled="c.already_imported" @change="toggleContainer(c)" />
                  <div class="res-info">
                    <div class="res-name">
                      <span class="mono trunc" :title="c.name">{{ c.name }}</span>
                      <span class="badge" :class="c.state === 'running' ? 'badge-success' : 'badge-muted'">{{ c.state }}</span>
                      <span v-if="c.already_imported" class="badge badge-info">already imported</span>
                    </div>
                    <div class="res-meta text-muted">
                      <code>{{ c.image }}<template v-if="c.tag">:{{ c.tag }}</template></code>
                      <span v-if="fmtPorts(c)" title="ports">· {{ fmtPorts(c) }}</span>
                      <span v-if="c.env_count" title="environment variables">· {{ c.env_count }} env</span>
                      <span v-if="c.volumes?.length" title="volumes">· {{ c.volumes.length }} vol</span>
                      <span v-if="c.networks?.length" title="networks">· {{ c.networks.length }} net</span>
                      <span v-if="c.restart_policy" title="restart policy">· {{ c.restart_policy }}</span>
                    </div>
                    <div v-if="c.secret_env_keys?.length" class="res-meta secret-hint">
                      <span class="mdi mdi-key-variant"></span> secret-looking env: {{ c.secret_env_keys.join(', ') }}
                    </div>
                  </div>
                </label>
                <input
                  v-if="selContainers.has(c.id)"
                  v-model="appNames[c.id]"
                  class="form-input form-input-sm app-name"
                  placeholder="app name"
                  aria-label="App name"
                />
              </div>
            </div>
          </div>
        </div>

        <!-- Volumes -->
        <div v-if="resources.volumes.length" class="card mb-4">
          <div class="card-header"><h2>Volumes <span class="text-muted" style="font-weight: 400">{{ resources.volumes.length }}</span></h2></div>
          <div class="card-body">
            <label v-for="v in resources.volumes" :key="v.name" class="res-row" :class="{ disabled: v.already_imported }">
              <input type="checkbox" :checked="selVolumes.has(v.name)" :disabled="v.already_imported" @change="toggleSet('vol', v.name, v.already_imported)" />
              <div class="res-info">
                <div class="res-name"><span class="mono trunc" :title="v.name">{{ v.name }}</span>
                  <span v-if="v.already_imported" class="badge badge-info">already imported</span>
                </div>
                <div class="res-meta text-muted">{{ v.driver }}<span v-if="v.used_by?.length"> · used by {{ v.used_by.join(', ') }}</span></div>
              </div>
            </label>
          </div>
        </div>

        <!-- Networks -->
        <div v-if="resources.networks.length" class="card mb-4">
          <div class="card-header"><h2>Networks <span class="text-muted" style="font-weight: 400">{{ resources.networks.length }}</span></h2></div>
          <div class="card-body">
            <label v-for="n in resources.networks" :key="n.name" class="res-row" :class="{ disabled: n.already_imported }">
              <input type="checkbox" :checked="selNetworks.has(n.name)" :disabled="n.already_imported" @change="toggleSet('net', n.name, n.already_imported)" />
              <div class="res-info">
                <div class="res-name"><span class="mono trunc" :title="n.name">{{ n.name }}</span>
                  <span v-if="n.already_imported" class="badge badge-info">already imported</span>
                </div>
                <div class="res-meta text-muted">{{ n.driver }}<span v-if="n.used_by?.length"> · used by {{ n.used_by.join(', ') }}</span></div>
              </div>
            </label>
          </div>
        </div>

        <!-- Sticky action bar -->
        <div class="action-bar">
          <span class="text-muted sel-count">{{ selectedCount }} selected</span>
          <button class="btn btn-secondary" @click="goToNode">Cancel</button>
          <button class="btn btn-primary" :disabled="!canImport" @click="doImport">
            {{ importing ? 'Importing…' : 'Import' }}
          </button>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.node-import { padding-bottom: 72px; }
.text-muted { color: var(--text-muted); }
.back-link { display: inline-flex; align-items: center; gap: 4px; color: var(--text-muted); font-size: 13px; text-decoration: none; margin-bottom: 4px; }
.back-link:hover { color: var(--text); }
.intro { margin-bottom: 16px; font-size: 13px; max-width: 720px; }
.target { display: flex; gap: 16px; flex-wrap: wrap; }
.target .form-group { flex: 1; min-width: 200px; margin: 0; }
.compose-group { margin-bottom: 10px; }
.compose-label { font-size: 12px; color: var(--text-muted); margin: 8px 0 4px; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.stack-arrow { font-size: 13px; opacity: .6; }
.stack-inline { display: inline-flex; align-items: center; gap: 4px; }
.stack-name { width: 150px; padding: 3px 7px; font-size: 12px; }
.res-row { display: flex; align-items: flex-start; gap: 10px; padding: 8px 10px; border: 1px solid var(--border-primary); border-radius: 8px; margin-bottom: 6px; cursor: pointer; }
.res-row.disabled { opacity: .55; cursor: default; }
.res-row input[type=checkbox] { margin-top: 3px; }
.res-main { display: flex; align-items: flex-start; gap: 10px; flex: 1; cursor: inherit; }
.res-info { flex: 1; min-width: 0; }
.res-name { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.res-meta { font-size: 12px; margin-top: 2px; display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.secret-hint { color: var(--warning, #b7791f); }
.app-name { width: 160px; flex-shrink: 0; }
.trunc { max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.badge-muted { background: var(--bg-tertiary); color: var(--text-muted); }
.form-input-sm { padding: 5px 8px; font-size: 13px; }
/* Sticky bottom action bar so Import stays reachable on long lists. */
.action-bar { position: sticky; bottom: 0; display: flex; align-items: center; gap: 8px; justify-content: flex-end; padding: 12px 16px; background: var(--bg-secondary, var(--bg)); border-top: 1px solid var(--border-primary); border-radius: 8px; box-shadow: 0 -2px 8px rgba(0,0,0,.04); }
.sel-count { margin-right: auto; font-size: 13px; }
</style>
