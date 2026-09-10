<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useNotificationStore } from '@/stores/notification'
import { adminApi, type ImageCatalogItem } from '@/api/admin'

const notify = useNotificationStore()

const images = ref<ImageCatalogItem[]>([])
const mirror = ref('')
// The mirror is Enterprise (private_registry); the per-image overrides below are
// not. A mirror already set stays in effect either way — only editing is gated.
const mirrorEditable = ref(true)
const overrides = ref<Record<string, string>>({})
const loading = ref(false)
const saving = ref(false)

async function load() {
  loading.value = true
  try {
    const cfg = (await adminApi.getDeploymentConfig()).data.data
    images.value = cfg.images ?? []
    mirror.value = cfg.mirror ?? ''
    mirrorEditable.value = cfg.mirror_editable !== false
    overrides.value = Object.fromEntries(images.value.map((i) => [i.key, i.override || '']))
  } catch (e) { notify.apiError(e) }
  finally { loading.value = false }
}
onMounted(load)

// Group images by category, preserving first-seen order.
const groups = computed(() => {
  const m = new Map<string, ImageCatalogItem[]>()
  for (const i of images.value) {
    if (!m.has(i.category)) m.set(i.category, [])
    m.get(i.category)!.push(i)
  }
  return Array.from(m, ([category, items]) => ({ category, items }))
})

// The effective ref previewed live as the admin types (override || default, with mirror).
function preview(item: ImageCatalogItem): string {
  const base = (overrides.value[item.key] || '').trim() || item.default
  const m = mirror.value.trim().replace(/\/+$/, '')
  if (!m || !base) return base
  // Don't prefix a ref that already names a registry host (first segment has . or :).
  const first = base.split('/')[0]
  if (base.includes('/') && /[.:]/.test(first)) return base
  return `${m}/${base}`
}

async function save() {
  saving.value = true
  try {
    await adminApi.updateDeploymentConfig(mirror.value.trim(), overrides.value)
    notify.success('Deployment config saved')
    load()
  } catch (e) { notify.apiError(e) }
  finally { saving.value = false }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Deployment Config</h1>
        <div class="text-muted text-sm">Images the platform runs. Changes apply to new provisions/deploys — running containers keep their image until redeployed.</div>
      </div>
      <button class="btn btn-primary" :disabled="saving" @click="save">{{ saving ? 'Saving…' : 'Save changes' }}</button>
    </div>

    <div v-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else>
      <div class="card mb-4">
        <div class="card-header">
          <h2>Registry mirror</h2>
          <span v-if="!mirrorEditable" class="badge badge-neutral">
            <span class="mdi mdi-lock-outline"></span> Enterprise
          </span>
        </div>
        <div class="card-body">
          <input
            v-model="mirror"
            class="form-input"
            placeholder="e.g. registry.internal/proxy (blank = none)"
            aria-label="Registry mirror"
            :disabled="!mirrorEditable"
            style="max-width: 420px; font-family: monospace"
          />
          <p class="text-muted text-sm" style="margin-top: 6px">Prefixes every image not already qualified with a registry host — for private/air-gapped installs.</p>
          <p v-if="!mirrorEditable" class="text-muted text-sm" style="margin-top: 6px">
            Changing the mirror needs an Enterprise licence. A mirror already set keeps working and
            still prefixes every image below — the per-image overrides remain editable.
          </p>
        </div>
      </div>

      <div v-for="g in groups" :key="g.category" class="card mb-4">
        <div class="card-header"><h2>{{ g.category }}</h2></div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Image</th><th>Override</th><th>Effective</th></tr></thead>
            <tbody>
              <tr v-for="item in g.items" :key="item.key">
                <td>
                  <span class="cell-title">{{ item.label }}</span>
                  <div class="cell-sub">{{ item.description }}</div>
                  <div class="cell-sub" style="font-family: monospace">default: {{ item.default }}</div>
                </td>
                <td>
                  <input v-model="overrides[item.key]" class="form-input" :placeholder="item.default" :aria-label="`Image override for ${item.label}`" style="min-width: 240px; font-family: monospace" />
                </td>
                <td class="cell-sub" style="font-family: monospace; word-break: break-all">{{ preview(item) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
</style>
