import type { Router } from 'vue-router'

// Announcements often link outside the app (status page, release notes), which router.push cannot resolve.
export function followNotificationLink(router: Router, link?: string | null): void {
  if (!link) return
  if (/^https?:\/\//i.test(link)) {
    window.open(link, '_blank', 'noopener')
    return
  }
  void router.push(link)
}
