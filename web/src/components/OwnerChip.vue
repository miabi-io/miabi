<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

// Owner lives under the reserved miabi.io/owner-kind|id|name keys (see models.SetOwner).
const props = defineProps<{ metadata?: Record<string, string> | null }>()
const { t } = useI18n()

const KIND = 'miabi.io/owner-kind'
const ID = 'miabi.io/owner-id'
const NAME = 'miabi.io/owner-name'

interface Owner {
  kind: string
  id: number
  name: string
  icon: string
  to: string | null
  label: string
}

const owner = computed<Owner | null>(() => {
  const md = props.metadata || {}
  const kind = md[KIND]
  if (!kind) return null
  const id = Number(md[ID] || 0)
  const name = md[NAME] || ''
  const meta: Record<string, { icon: string; route: string; noun: string }> = {
    app: { icon: 'mdi-cube-outline', route: 'apps', noun: 'owner.noun.app' },
    database: { icon: 'mdi-database', route: 'databases', noun: 'owner.noun.database' },
    stack: { icon: 'mdi-layers-outline', route: 'stacks', noun: 'owner.noun.stack' },
    user: { icon: 'mdi-account-outline', route: '', noun: 'owner.noun.user' },
  }
  const m = meta[kind] || { icon: 'mdi-help-circle-outline', route: '', noun: '' }
  const to = m.route && id ? `/${m.route}/${id}` : null
  const noun = m.noun ? t(m.noun) : kind
  const label = name || (kind === 'user' ? (id ? t('owner.memberId', { id }) : t('owner.aMember')) : `${noun} #${id}`)
  return { kind, id, name, icon: m.icon, to, label }
})
</script>

<template>
  <span v-if="owner" class="owner-chip" :title="$t('owner.title', { name: owner.label })">
    <span class="mdi" :class="owner.icon"></span>
    <RouterLink v-if="owner.to" :to="owner.to" class="owner-link">{{ owner.label }}</RouterLink>
    <span v-else>{{ owner.label }}</span>
  </span>
  <span v-else class="owner-none">—</span>
</template>

<style scoped>
.owner-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
}
.owner-chip .mdi {
  color: var(--text-muted);
  font-size: 15px;
}
.owner-link {
  color: var(--text-primary);
}
.owner-link:hover {
  text-decoration: underline;
}
.owner-none {
  color: var(--text-muted);
}
</style>
