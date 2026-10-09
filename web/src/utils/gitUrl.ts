const REPO_URL = /^(?:https?:\/\/[^/\s]+\/[^/\s]+\/[^/\s]+\S*|git@[^:\s]+:[^/\s]+\/[^/\s]+\S*|ssh:\/\/\S+\/\S+)$/

/**
 * Whether a typed clone URL names a whole repository (host, owner and name), so it is
 * worth probing. Half-typed input like `https://github.com/acme` is rejected.
 */
export function looksLikeRepoUrl(url: string): boolean {
  return REPO_URL.test(url.trim())
}
