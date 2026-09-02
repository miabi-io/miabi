<script setup lang="ts">
import type { DNSProviderField } from '@/api/types'

const props = defineProps<{ field: DNSProviderField; modelValue: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

function set(v: string) {
  emit('update:modelValue', v)
}
</script>

<template>
  <div class="form-group">
    <label class="form-label" :for="`dnsf-${props.field.key}`">
      {{ props.field.label }}
      <span v-if="!props.field.required" class="optional">optional</span>
    </label>

    <textarea
      v-if="props.field.type === 'textarea'"
      :id="`dnsf-${props.field.key}`"
      class="form-input mono"
      rows="5"
      :placeholder="props.field.placeholder"
      :value="props.modelValue"
      @input="set(($event.target as HTMLTextAreaElement).value)"
    ></textarea>

    <select
      v-else-if="props.field.type === 'enum'"
      :id="`dnsf-${props.field.key}`"
      class="form-input"
      :value="props.modelValue"
      @change="set(($event.target as HTMLSelectElement).value)"
    >
      <option v-for="o in props.field.options ?? []" :key="o" :value="o">{{ o }}</option>
    </select>

    <input
      v-else
      :id="`dnsf-${props.field.key}`"
      class="form-input mono"
      :type="props.field.type === 'password' ? 'password' : 'text'"
      :autocomplete="props.field.type === 'password' ? 'new-password' : 'off'"
      :placeholder="props.field.placeholder"
      :value="props.modelValue"
      @input="set(($event.target as HTMLInputElement).value)"
    />

    <p v-if="props.field.help" class="form-hint">{{ props.field.help }}</p>
  </div>
</template>

<style scoped>
.mono { font-family: 'JetBrains Mono', monospace; font-size: 13px; }
.optional { font-weight: 400; font-size: 11px; color: var(--text-muted); margin-left: 6px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
</style>
