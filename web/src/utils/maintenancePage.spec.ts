import { describe, expect, it } from 'vitest'
import { htmlBytes, maintenancePageTemplate } from './maintenancePage'

describe('maintenancePageTemplate', () => {
  it('escapes what it fills in', () => {
    const page = maintenancePageTemplate({ title: 'a<b', heading: 'Back soon', message: '<script>x</script> & more' })
    expect(page).toContain('<title>a&lt;b</title>')
    expect(page).toContain('&lt;script&gt;x&lt;/script&gt; &amp; more')
    expect(page).not.toContain('<script>')
  })

  it('needs nothing from the network', () => {
    const page = maintenancePageTemplate({ title: 't', heading: 'h', message: 'm' })
    expect(page).not.toMatch(/(src|href)=/)
    expect(page).not.toMatch(/@import|url\(/)
  })
})

describe('htmlBytes', () => {
  it('counts trimmed UTF-8 bytes, as the server does', () => {
    expect(htmlBytes('  é \n')).toBe(2)
  })
})
