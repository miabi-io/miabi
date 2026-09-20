<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { pipelineApi } from '@/api/pipelines'
import { appApi } from '@/api/apps'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { copyText } from '@/utils/clipboard'
import { parseYaml } from '@/utils/yaml'
import type { PipelineDefinition } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const props = defineProps<{ open: boolean; pipeline: PipelineDefinition | null }>()
const emit = defineEmits<{ close: [] }>()

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const loading = ref(false)
const info = ref<{ path: string; secret: string; signature_header: string } | null>(null)
const repoUrl = ref('')
const revealed = ref(false)

type Provider = 'github' | 'gitlab' | 'other'
const provider = ref<Provider>('github')

/**
 * The webhook URL a Git provider posts to. The path from the API is absolute
 * from the root and already carries /api/v1, so it is resolved against the API's
 * origin — which is the console's origin unless the API is hosted elsewhere.
 */
function absoluteUrl(path: string) {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? new URL(base).origin : window.location.origin
  return new URL(path, origin).toString()
}

const webhookUrl = computed(() => (info.value ? absoluteUrl(info.value.path) : ''))
const secret = computed(() => info.value?.secret ?? '')
const maskedSecret = computed(() => (secret.value ? '•'.repeat(Math.min(secret.value.length, 40)) : ''))

// --- What the spec actually declares ---
//
// A webhook that is wired correctly but fires nothing is the worst outcome here:
// the provider reports a 200, and the pipeline never runs. So the spec's triggers
// are read up front and any mismatch is stated before the user leaves to go
// configure the provider.

interface Triggers {
  push: boolean
  branches: string[]
  parseFailed: boolean
}

const triggers = computed<Triggers>(() => {
  const spec = props.pipeline?.spec
  if (!spec) return { push: false, branches: [], parseFailed: false }
  try {
    const doc = parseYaml(spec) as { on?: { push?: { branches?: unknown } | null } }
    const push = doc?.on?.push
    if (push === undefined || push === null) return { push: false, branches: [], parseFailed: false }
    const raw = (push as { branches?: unknown }).branches
    const branches = Array.isArray(raw) ? raw.map(String) : []
    return { push: true, branches, parseFailed: false }
  } catch {
    // A spec Miabi can't read is a separate problem; don't claim anything about it.
    return { push: false, branches: [], parseFailed: true }
  }
})

const branchLabel = computed(() => {
  const b = triggers.value.branches
  if (!b.length) return 'any branch'
  return b.length === 1 ? `the ${b[0]} branch` : `the ${b.slice(0, -1).join(', ')} and ${b[b.length - 1]} branches`
})

/** Blocking reasons a push would not start a run, most important first. */
const blockers = computed(() => {
  const out: string[] = []
  if (props.pipeline && !props.pipeline.enabled) {
    out.push('This pipeline is disabled — pushes are accepted but no run starts until you enable it.')
  }
  if (triggers.value.parseFailed) {
    out.push("This pipeline's spec could not be read, so its triggers are unknown.")
  } else if (!triggers.value.push) {
    out.push(
      'This pipeline has no `on.push` trigger. The webhook will be accepted and then ignored — ' +
        'add a push trigger to the spec to make it fire.',
    )
  }
  return out
})

// --- Provider deep link ---

/** Where to add the webhook in the provider's UI, when the repo URL says enough. */
const providerLink = computed(() => {
  const slug = repoSlug(repoUrl.value)
  if (!slug) return null
  if (slug.host.includes('github')) {
    return { label: t('pipelines.openGithubWebhooks'), href: `https://${slug.host}/${slug.path}/settings/hooks/new` }
  }
  if (slug.host.includes('gitlab')) {
    return { label: t('pipelines.openGitlabWebhooks'), href: `https://${slug.host}/${slug.path}/-/hooks` }
  }
  return null
})

/** Split an HTTPS or SSH clone URL into its host and owner/repo path. */
function repoSlug(url: string): { host: string; path: string } | null {
  const u = url.trim().replace(/\.git$/i, '')
  if (!u) return null
  const ssh = u.match(/^[\w.-]+@([\w.-]+):(.+)$/) // git@host:owner/repo
  if (ssh) return { host: ssh[1], path: ssh[2] }
  try {
    const parsed = new URL(u)
    const path = parsed.pathname.replace(/^\/+|\/+$/g, '')
    return path ? { host: parsed.host, path } : null
  } catch {
    return null
  }
}

// Pick the provider tab that matches the repository, so the right instructions
// are showing before the user picks anything.
watch(repoUrl, (url) => {
  const slug = repoSlug(url)
  if (!slug) return
  if (slug.host.includes('github')) provider.value = 'github'
  else if (slug.host.includes('gitlab')) provider.value = 'gitlab'
})

async function load() {
  const wid = currentWorkspaceId.value
  const p = props.pipeline
  if (!wid || !p) return
  loading.value = true
  revealed.value = false
  info.value = null
  repoUrl.value = ''
  try {
    info.value = (await pipelineApi.webhookInfo(wid, p.id)).data.data
  } catch (e) {
    notify.apiError(e, 'Could not load the webhook details')
  } finally {
    loading.value = false
  }
  // The bound app's repository drives the deep link. Best-effort: without it the
  // modal still shows everything needed to configure the hook by hand.
  if (p.application_id) {
    try {
      repoUrl.value = (await appApi.get(wid, p.application_id)).data.data.git_repo ?? ''
    } catch {
      repoUrl.value = ''
    }
  }
}

watch(() => [props.open, props.pipeline?.id], () => { if (props.open) load() }, { immediate: true })

async function copy(text: string) {
  if (await copyText(text)) notify.success(t('notify.common.copiedToClipboard'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="open" max-width="640px" @close="emit('close')">
      <div class="modal-header">
        <h3>{{ $t('pipelines.pushWebhook') }}</h3>
        <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="emit('close')">
          <span class="mdi mdi-close"></span>
        </button>
      </div>

      <div class="modal-body">
        <div v-if="loading" class="loading"><span class="spinner"></span></div>

        <template v-else-if="info">
          <i18n-t :keypath="triggers.push ? 'pipelines.webhookLeadBranch' : 'pipelines.webhookLead'" tag="p" class="lead">
            <template #branch><strong>{{ branchLabel }}</strong></template>
            <template #pipeline><strong>{{ pipeline?.display_name || pipeline?.name }}</strong></template>
          </i18n-t>

          <div v-for="(b, i) in blockers" :key="i" class="warn">
            <span class="mdi mdi-alert-outline"></span>
            <span>{{ b }}</span>
          </div>

          <div class="field">
            <label class="form-label">{{ $t('pipelines.payloadUrl') }}</label>
            <div class="copy-row">
              <code class="mono">{{ webhookUrl }}</code>
              <button class="btn-icon btn-icon-muted" :title="$t('pipelines.copyUrl')" :aria-label="$t('pipelines.copyUrl')" @click="copy(webhookUrl)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
          </div>

          <div class="field">
            <label class="form-label">{{ $t('pipelines.secret') }}</label>
            <div class="copy-row">
              <code class="mono">{{ revealed ? secret : maskedSecret }}</code>
              <button
                class="btn-icon btn-icon-muted"
                :title="revealed ? $t('pipelines.hideSecret') : $t('pipelines.revealSecret')"
                :aria-label="revealed ? $t('pipelines.hideSecret') : $t('pipelines.revealSecret')"
                @click="revealed = !revealed"
              >
                <span class="mdi" :class="revealed ? 'mdi-eye-off-outline' : 'mdi-eye-outline'"></span>
              </button>
              <button class="btn-icon btn-icon-muted" :title="$t('pipelines.copySecret')" :aria-label="$t('pipelines.copySecret')" @click="copy(secret)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
            <p class="form-hint">{{ $t('pipelines.secretHint') }}</p>
          </div>

          <div class="tabs provider-tabs">
            <button class="tab" :class="{ active: provider === 'github' }" @click="provider = 'github'">{{ $t('pipelines.github') }}</button>
            <button class="tab" :class="{ active: provider === 'gitlab' }" @click="provider = 'gitlab'">{{ $t('pipelines.gitlab') }}</button>
            <button class="tab" :class="{ active: provider === 'other' }" @click="provider = 'other'">{{ $t('pipelines.other') }}</button>
          </div>

          <ol v-if="provider === 'github'" class="steps">
            <li><i18n-t keypath="pipelines.ghStep1" tag="span"><template #menu><strong>{{ $t('pipelines.settingsWebhooksAddWebhook') }}</strong></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.ghStep2" tag="span"><template #field><strong>{{ $t('pipelines.payloadUrl') }}</strong></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.ghStep3" tag="span"><template #field><strong>{{ $t('pipelines.contentType') }}</strong></template><template #value><code class="mono">application/json</code></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.ghStep4" tag="span"><template #field><strong>{{ $t('pipelines.secret') }}</strong></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.ghStep5" tag="span"><template #option><strong>{{ $t('pipelines.justThePushEvent') }}</strong></template></i18n-t></li>
          </ol>

          <ol v-else-if="provider === 'gitlab'" class="steps">
            <li><i18n-t keypath="pipelines.glStep1" tag="span"><template #menu><strong>{{ $t('pipelines.settingsWebhooksAddNewWebhook') }}</strong></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.glStep2" tag="span"><template #field><strong>{{ $t('pipelines.payloadUrl') }}</strong></template><template #target><strong>URL</strong></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.glStep3" tag="span"><template #field><strong>{{ $t('pipelines.secret') }}</strong></template><template #target><strong>{{ $t('pipelines.secretToken') }}</strong></template></i18n-t></li>
            <li><i18n-t keypath="pipelines.glStep4" tag="span"><template #option><strong>{{ $t('pipelines.pushEvents') }}</strong></template></i18n-t></li>
          </ol>

          <div v-else class="other-provider">
            <p class="text-sm">{{ $t('pipelines.otherProviderHint') }}</p>
            <ul class="scheme-list">
              <li><i18n-t keypath="pipelines.schemeHmac" tag="span"><template #header><code class="mono">{{ info.signature_header }}: sha256=&lt;hmac&gt;</code></template></i18n-t></li>
              <li><i18n-t keypath="pipelines.schemeToken" tag="span"><template #header><code class="mono">X-Gitlab-Token: &lt;secret&gt;</code></template></i18n-t></li>
            </ul>
            <i18n-t keypath="pipelines.payloadFieldsHint" tag="p" class="text-sm">
              <template #ref><code class="mono">ref</code></template>
              <template #a><code class="mono">head_commit.id</code></template>
              <template #b><code class="mono">after</code></template>
              <template #c><code class="mono">checkout_sha</code></template>
            </i18n-t>
          </div>

          <a
            v-if="providerLink"
            class="btn btn-secondary btn-sm provider-link"
            :href="providerLink.href"
            target="_blank"
            rel="noopener"
          >
            <span class="mdi mdi-open-in-new"></span> {{ providerLink.label }}
          </a>
        </template>

        <p v-else class="text-muted text-sm">{{ $t('pipelines.webhookLoadFailed') }}</p>
      </div>

      <div class="modal-footer">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ $t('shell.close') }}</button>
      </div>
    </AppModal>
  </Teleport>
</template>

<style scoped>
.loading { display: flex; justify-content: center; padding: 24px 0; }
.lead { font-size: 13px; color: var(--text-secondary, var(--text-muted)); margin: 0 0 16px; line-height: 1.6; }
.field { margin-bottom: 16px; }
.copy-row {
  display: flex; align-items: center; gap: 4px; padding: 6px 6px 6px 12px;
  background: var(--bg-tertiary); border: 1px solid var(--border-primary); border-radius: var(--radius);
}
.copy-row code { flex: 1; min-width: 0; overflow-x: auto; white-space: nowrap; font-size: 12px; }
.warn {
  display: flex; gap: 8px; align-items: flex-start; margin-bottom: 12px; padding: 10px 12px;
  border-radius: var(--radius); font-size: 13px; line-height: 1.5;
  background: var(--warning-50, rgba(234, 179, 8, 0.1));
  color: var(--warning-700, var(--text-primary));
  border: 1px solid var(--warning-200, rgba(234, 179, 8, 0.3));
}
.warn .mdi { font-size: 16px; flex-shrink: 0; margin-top: 1px; }
.provider-tabs { margin: 20px 0 12px; }
.steps { margin: 0; padding-left: 20px; font-size: 13px; line-height: 1.9; color: var(--text-secondary, var(--text-muted)); }
.other-provider { font-size: 13px; color: var(--text-secondary, var(--text-muted)); line-height: 1.6; }
.scheme-list { margin: 8px 0; padding-left: 20px; display: flex; flex-direction: column; gap: 6px; }
.provider-link { margin-top: 16px; }
.mono { font-family: 'JetBrains Mono', monospace; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
</style>
