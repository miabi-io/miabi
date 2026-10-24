import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { adminApi } from '@/api/admin'
import type { BillingAPIState } from '@/api/types'

// Platform switches the admin console shows or hides pages for (GET /admin/info).
export const useAdminInfoStore = defineStore('adminInfo', () => {
  const billingApi = ref<BillingAPIState>('off')
  const billingVisible = computed(() => billingApi.value !== 'off')

  let inflight: Promise<void> | null = null
  function load(force = false): Promise<void> {
    if (inflight && !force) return inflight
    inflight = adminApi.info()
      .then((res) => { billingApi.value = res.data.data.billing_api })
      .catch(() => { inflight = null })
    return inflight
  }

  return { billingApi, billingVisible, load }
})
