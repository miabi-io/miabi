import { describe, expect, it } from 'vitest'
import { MAINTENANCE_HTML_MAX, htmlBytes, maintenancePageTemplate } from './maintenancePage'

const labels = { badge: 'Scheduled maintenance', checkAgain: 'Check again', refreshHint: 'Refreshes every minute.', hostedOn: 'Hosted on' }

describe('maintenancePageTemplate', () => {
  it('escapes what it fills in', () => {
    const page = maintenancePageTemplate({ app: 'a<b', heading: 'Back soon', message: '<script>x</script> & more', labels })
    expect(page).toContain('<title>a&lt;b · Scheduled maintenance</title>')
    expect(page).toContain('&lt;script&gt;x&lt;/script&gt; &amp; more')
    expect(page).not.toContain('<script>')
  })

  it('needs nothing from the network', () => {
    const page = maintenancePageTemplate({ app: 'a', heading: 'h', message: 'm', labels })
    expect(page).not.toMatch(/(src|href|action)=/)
    expect(page).not.toMatch(/@import|url\((?!#)/)
  })

  it('stays far below the size cap', () => {
    const page = maintenancePageTemplate({ app: 'a', heading: 'h', message: 'm', labels })
    expect(htmlBytes(page)).toBeLessThan(MAINTENANCE_HTML_MAX / 8)
  })
})

describe('htmlBytes', () => {
  it('counts trimmed UTF-8 bytes, as the server does', () => {
    expect(htmlBytes('  é \n')).toBe(2)
  })
})
