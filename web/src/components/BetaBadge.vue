<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useComponents } from '@/composables/useComponents'

// Promoting the component to Stable removes the badge everywhere without touching the pages.
const props = defineProps<{ component: string }>()

const { t } = useI18n()
const { byId } = useComponents()
const info = computed(() => byId(props.component))
const early = computed(() => info.value?.stability === 'beta' || info.value?.stability === 'preview')
const tip = computed(() =>
  info.value ? `${t(`beta.title.${info.value.stability}`, { name: info.value.name })} ${t(`beta.body.${info.value.stability}`)}` : '',
)
</script>

<template>
  <span v-if="early && info" class="beta-badge" :class="`is-${info.stability}`" :title="tip" tabindex="0" :aria-label="tip">
    {{ $t(`beta.label.${info.stability}`) }}
  </span>
</template>

<style scoped>
.beta-badge {
  display: inline-flex;
  align-items: center;
  margin-left: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  line-height: 1.5;
  vertical-align: middle;
  cursor: help;
  background: var(--warning-50);
  color: var(--warning-600);
  border: 1px solid var(--warning-600);
}
.beta-badge.is-preview {
  background: var(--bg-tertiary);
  color: var(--text-secondary);
  border-color: var(--border-primary);
}
</style>
