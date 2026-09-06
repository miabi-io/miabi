import type { Router } from 'vue-router'

// followNotificationLink opens a notification's link. Alerts always deep-link
// inside the app, but an announcement often points at something outside it — a
// status page, release notes — which router.push cannot resolve.
export function followNotificationLink(router: Router, link?: string | null): void {
  if (!link) return
  if (/^https?:\/\//i.test(link)) {
    window.open(link, '_blank', 'noopener')
    return
  }
  void router.push(link)
}
