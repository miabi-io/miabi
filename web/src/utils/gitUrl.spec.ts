import { describe, expect, it } from 'vitest'
import { looksLikeRepoUrl } from './gitUrl'

describe('looksLikeRepoUrl', () => {
  it.each([
    'https://github.com/acme/web',
    'https://github.com/acme/web.git',
    'https://gitlab.example.com/group/sub/project',
    'http://gitea.local:3000/acme/web',
    'git@github.com:acme/web.git',
    'ssh://git@github.com/acme/web.git',
    '  https://github.com/acme/web  ',
  ])('accepts %s', (url) => {
    expect(looksLikeRepoUrl(url)).toBe(true)
  })

  it.each([
    '',
    'https://',
    'https://github.com',
    'https://github.com/',
    'https://github.com/acme',
    'https://github.com/acme/',
    'git@github.com:acme',
    'github.com/acme/web',
    'https://github.com/acme/my web',
  ])('rejects %s', (url) => {
    expect(looksLikeRepoUrl(url)).toBe(false)
  })
})
