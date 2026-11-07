<script setup lang="ts">
import { ref } from 'vue'
import AppModal from '@/components/AppModal.vue'
import { copyText } from '@/utils/clipboard'
import { useNotificationStore } from '@/stores/notification'

// Not stored in clear, so the admin copies it now and hands it over out-of-band.
defineProps<{ title: string; password: string }>()
const emit = defineEmits<{ close: [] }>()

const notify = useNotificationStore()
const copied = ref(false)

async function copy(password: string) {
  if (await copyText(password)) {
    copied.value = true
    notify.success('Password copied')
  }
}
</script>

<template>
  <AppModal @close="emit('close')">
    <div class="modal-header">
      <h2>{{ title }}</h2>
      <button class="btn-icon" aria-label="Close" @click="emit('close')"><span class="mdi mdi-close"></span></button>
    </div>
    <div class="modal-body">
      <p class="text-muted" style="margin-top: 0">
        <slot />
        Copy it now and share it with the user through a secure channel — <strong>it won't be shown again</strong>.
      </p>
      <div class="pw-reveal">
        <code class="pw-value">{{ password }}</code>
        <button class="btn btn-secondary btn-sm" @click="copy(password)">
          <span class="mdi" :class="copied ? 'mdi-check' : 'mdi-content-copy'"></span>
          {{ copied ? 'Copied' : 'Copy' }}
        </button>
      </div>
    </div>
    <div class="modal-footer">
      <button class="btn btn-primary btn-sm" @click="emit('close')">Done</button>
    </div>
  </AppModal>
</template>

<style scoped>
.pw-reveal {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
  background: var(--surface-2, rgba(127, 127, 127, 0.06));
}
.pw-value {
  flex: 1;
  font-family: var(--font-mono, monospace);
  font-size: 15px;
  letter-spacing: 0.5px;
  user-select: all;
  word-break: break-all;
}
</style>
