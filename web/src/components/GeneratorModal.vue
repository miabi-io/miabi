<script setup lang="ts">
import { ref } from 'vue'
import AppModal from '@/components/AppModal.vue'
import GeneratorPanel from '@/components/GeneratorPanel.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title?: string
  }>(),
  {
    title: 'Generate value',
  },
)

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'use', value: string): void
}>()

const currentValue = ref('')

function use(val?: string) {
  const targetValue = val || currentValue.value
  if (!targetValue) return
  emit('use', targetValue)
  emit('close')
}
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="open" elevated max-width="400px" @close="emit('close')">
      <div class="modal-header">
        <h3>{{ title }}</h3>
        <button type="button" class="btn-icon btn-icon-muted" aria-label="Close dialog" @click="emit('close')">
          <span class="mdi mdi-close"></span>
        </button>
      </div>

      <div class="modal-body">
        <GeneratorPanel @use="use">
          <template #actions="{ value }">
            <span :class="{ hidden: (currentValue = value) }"></span>
          </template>
        </GeneratorPanel>
      </div>

      <div class="modal-footer">
        <button type="button" class="btn btn-secondary" @click="emit('close')">Cancel</button>
        <button type="button" class="btn btn-primary" :disabled="!currentValue" @click="use()">
          Use this value
        </button>
      </div>
    </AppModal>
  </Teleport>
</template>

<style scoped>
.hidden {
  display: none;
}
</style>