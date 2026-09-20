<script setup lang="ts">
// A cluster or node that belongs to one organization. Renders nothing otherwise: a "shared" badge
// on every row of an ordinary single-tenant install is noise, and absence already says it.
//
// `via` names the cluster a node inherits this from, so the badge cannot be read as the node having
// been assigned to the organization by itself.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ dedicated?: boolean; organization?: string; via?: string }>()

const { t } = useI18n()

const label = computed(() =>
  props.organization ? `${t('dedicated.badge')} · ${props.organization}` : t('dedicated.badge'),
)

const title = computed(() => {
  const org = props.organization || t('dedicated.anOrganization')
  return props.via
    ? t('dedicated.nodeTitle', { org, cluster: props.via })
    : t('dedicated.clusterTitle', { org })
})
</script>

<template>
  <span v-if="dedicated" class="badge badge-success dedicated-badge" :title="title">
    <span class="mdi mdi-shield-check"></span>{{ label }}
  </span>
</template>

<style scoped>
.dedicated-badge { display: inline-flex; align-items: center; gap: 4px; }
</style>
