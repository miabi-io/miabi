const REPO_URL = /^(?:https?:\/\/[^/\s]+\/[^/\s]+\/[^/\s]+\S*|git@[^:\s]+:[^/\s]+\/[^/\s]+\S*|ssh:\/\/\S+\/\S+)$/

/** looksLikeRepoUrl rejects half-typed input like `https://github.com/acme`. */
export function looksLikeRepoUrl(url: string): boolean {
  return REPO_URL.test(url.trim())
}
