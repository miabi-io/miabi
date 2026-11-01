<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { gitRepositoryApi } from '@/api/gitRepositories'
import { appApi } from '@/api/apps'
import { pipelineApi } from '@/api/pipelines'
import { gitopsApi } from '@/api/gitops'
import type { Application, GitRepository, GitSource, PipelineDefinition } from '@/api/types'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const route = useRoute()
const { currentWorkspaceId } = storeToRefs(ws)

const repoId = computed(() => Number(route.params.id))
const repo = ref<GitRepository | null>(null)
const apps = ref<Application[]>([])
const pipelines = ref<PipelineDefinition[]>([])
const projects = ref<GitSource[]>([])
const testing = ref(false)

async function load() {
  const wid = currentWorkspaceId.value
  if (!wid || !repoId.value) return
  try {
    repo.value = (await gitRepositoryApi.get(wid, repoId.value)).data.data
  } catch (e) {
    notify.apiError(e)
    return
  }
  // Usage only enriches the page, so one list failing leaves its section empty rather than erroring.
  const [a, p, g] = await Promise.allSettled([appApi.list(wid), pipelineApi.list(wid, 0, 100), gitopsApi.list(wid)])
  apps.value = a.status === 'fulfilled' ? (a.value.data.data ?? []).filter((x) => x.git_repository_id === repoId.value) : []
  pipelines.value = p.status === 'fulfilled' ? (p.value.data.data ?? []).filter((x) => x.git_repository_id === repoId.value) : []
  projects.value = g.status === 'fulfilled' ? (g.value.data.data ?? []).filter((x) => x.git_repository_id === repoId.value) : []
}
watch([currentWorkspaceId, repoId], load, { immediate: true })

async function test() {
  const wid = currentWorkspaceId.value
  if (!wid || !repo.value) return
  testing.value = true
  try {
    repo.value = (await gitRepositoryApi.test(wid, repo.value.id)).data.data
    notify.success(t('notify.gitRepositories.connectionOk', { name: repo.value.name }))
  } catch (e) {
    notify.apiError(e, 'Connection failed')
    repo.value = (await gitRepositoryApi.get(wid, repoId.value).catch(() => null))?.data.data ?? repo.value
  } finally {
    testing.value = false
  }
}

const CONNECTION = {
  ok: { icon: 'mdi-check-circle', cls: 'conn-ok', label: 'gitRepos.conn.connected' },
  failed: { icon: 'mdi-alert-circle', cls: 'conn-failed', label: 'gitRepos.conn.failed' },
  unknown: { icon: 'mdi-help-circle-outline', cls: 'conn-unknown', label: 'gitRepos.conn.unchecked' },
} as const
const connection = computed(() => CONNECTION[repo.value?.connection_status ?? 'unknown'] ?? CONNECTION.unknown)
const unused = computed(() => !apps.value.length && !pipelines.value.length && !projects.value.length)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link :to="{ name: 'git-repositories' }" class="back-link"><span class="mdi mdi-arrow-left"></span>{{ $t('nav.sources.gitRepositories') }}</router-link>
        <h1>{{ repo?.display_name || repo?.name || $t('gitRepos.repository') }}</h1>
        <p v-if="repo" class="subtitle">
          <code>{{ repo.name }}</code> · <span class="mono">{{ repo.url }}</span> ·
          <span class="badge badge-neutral">{{ repo.auth_type }}</span>
          <template v-if="repo.secret_ref"> · <span class="mdi mdi-key-variant"></span> {{ repo.secret_ref }}</template>
        </p>
      </div>
      <div v-if="repo" class="header-actions">
        <span class="conn" :class="connection.cls" :title="repo.connection_error || ''">
          <span class="mdi" :class="connection.icon"></span>{{ $t(connection.label) }}</span>
        <button class="btn btn-secondary" :disabled="testing" @click="test">
          <span class="mdi" :class="testing ? 'mdi-loading mdi-spin' : 'mdi-connection'"></span>{{ $t('ldap.testConnection') }}</button>
      </div>
    </div>

    <div v-if="repo?.connection_status === 'failed' && repo.connection_error" class="card card-body conn-error">
      <span class="mdi mdi-alert-circle-outline"></span> {{ repo.connection_error }}
    </div>

    <div class="card mt-4">
      <div class="card-header"><h3>{{ $t('gitRepos.usedBy') }}</h3></div>
      <div v-if="unused" class="card-body cell-sub">{{ $t('gitRepos.unused') }}</div>
      <ul v-else class="usage">
        <li v-for="a in apps" :key="`a${a.id}`">
          <span class="mdi mdi-application-outline"></span>
          <router-link :to="{ name: 'app-detail', params: { id: a.id } }">{{ a.display_name || a.name }}</router-link>
          <span class="cell-sub">{{ $t('gitRepos.kindApp') }}<template v-if="a.git_ref"> · {{ a.git_ref }}</template></span>
        </li>
        <li v-for="p in pipelines" :key="`p${p.id}`">
          <span class="mdi mdi-pipe"></span>
          <router-link :to="{ name: 'pipeline-runs', params: { id: p.id } }">{{ p.display_name || p.name }}</router-link>
          <span class="cell-sub">{{ $t('gitRepos.kindPipeline') }}<template v-if="p.branch"> · {{ p.branch }}</template></span>
        </li>
        <li v-for="g in projects" :key="`g${g.id}`">
          <span class="mdi mdi-source-branch-sync"></span>
          <router-link :to="{ name: 'gitops-detail', params: { id: g.id } }">{{ g.display_name || g.name }}</router-link>
          <span class="cell-sub">{{ $t('gitRepos.kindGitops') }} · {{ g.ref }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.back-link { font-size: 13px; color: var(--text-muted); text-decoration: none; display: inline-flex; align-items: center; gap: 4px; }
.back-link:hover { color: var(--primary-500); }
.header-actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.conn { display: inline-flex; align-items: center; gap: 5px; font-size: 13px; }
.conn-ok { color: var(--success-500, #22c55e); }
.conn-failed { color: var(--danger-500, #ef4444); }
.conn-unknown { color: var(--text-muted); }
.conn-error { color: var(--danger-600, #dc2626); font-size: 13px; margin-top: 12px; }
.usage { list-style: none; margin: 0; padding: 4px 0; }
.usage li { display: flex; align-items: center; gap: 8px; padding: 8px 16px; font-size: 13px; border-top: 1px solid var(--border); }
.usage li:first-child { border-top: none; }
.usage .mdi { color: var(--text-muted); }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
