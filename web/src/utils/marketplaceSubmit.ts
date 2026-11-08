const ISSUE_URL = 'https://github.com/miabi-io/marketplace/issues/new'
// GitHub rejects request URLs beyond roughly 8 KB, so a larger manifest goes through the clipboard instead.
const MAX_URL = 7500

export function submitTemplateURL(yaml: string, name: string): { url: string; prefilled: boolean } {
  const params = new URLSearchParams({ template: 'submit-template.yml', title: `[template] ${name}`.trim() })
  const bare = `${ISSUE_URL}?${params}`
  params.set('template_yaml', yaml)
  const full = `${ISSUE_URL}?${params}`
  return full.length <= MAX_URL ? { url: full, prefilled: true } : { url: bare, prefilled: false }
}

export function manifestName(yaml: string): string {
  return /^ {2}name:\s*["']?([a-z0-9-]+)/m.exec(yaml)?.[1] ?? ''
}
