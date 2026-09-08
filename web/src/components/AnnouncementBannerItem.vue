<script setup lang="ts">
import { computed } from 'vue'
import type { AlertSeverity } from '@/api/inbox'

// The banner's presentation on its own, so the admin form can show an operator
// what a broadcast will look like using the same markup that will render it.
const props = defineProps<{
  severity: AlertSeverity
  title: string
  body?: string
  actionText?: string
  hasAction?: boolean
  dismissible?: boolean
}>()

defineEmits<{ (e: 'follow'): void; (e: 'dismiss'): void }>()

const sevClass = computed(() =>
  props.severity === 'critical' ? 'ban-crit' : props.severity === 'warning' ? 'ban-warn' : 'ban-info',
)
const sevIcon = computed(() =>
  props.severity === 'critical'
    ? 'mdi-alert-octagon'
    : props.severity === 'warning'
      ? 'mdi-alert'
      : 'mdi-bullhorn-outline',
)
</script>

<template>
  <div class="banner" :class="sevClass">
    <span class="mdi ban-icon" :class="sevIcon"></span>
    <div class="ban-body">
      <div class="ban-title">{{ title }}</div>
      <div v-if="body" class="ban-text">{{ body }}</div>
    </div>
    <button v-if="hasAction" class="btn btn-secondary btn-sm" @click="$emit('follow')">
      {{ actionText || 'Open' }}
    </button>
    <button v-if="dismissible" class="btn-icon btn-icon-muted" aria-label="Dismiss" @click="$emit('dismiss')">
      <span class="mdi mdi-close"></span>
    </button>
  </div>
</template>

<style scoped>
.banner {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid var(--border-primary);
  border-left-width: 3px;
  border-radius: 8px;
  background: var(--bg-secondary);
}

.ban-icon {
  font-size: 20px;
  flex-shrink: 0;
}

.ban-info {
  border-left-color: var(--primary-500);
}
.ban-info .ban-icon {
  color: var(--primary-500);
}

.ban-warn {
  border-left-color: var(--warning-600);
}
.ban-warn .ban-icon {
  color: var(--warning-600);
}

.ban-crit {
  border-left-color: var(--danger-500);
}
.ban-crit .ban-icon {
  color: var(--danger-500);
}

.ban-body {
  flex: 1;
  min-width: 0;
}

.ban-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}

.ban-text {
  font-size: 12px;
  color: var(--text-secondary);
  margin-top: 2px;
}
</style>
