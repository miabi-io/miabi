// The server speaks WebAuthn JSON (base64url); navigator.credentials wants ArrayBuffers.

export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials
}

function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(s.length / 4) * 4, '=')
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

function toB64url(buf: ArrayBuffer | null | undefined): string | undefined {
  if (!buf) return undefined
  const bytes = new Uint8Array(buf)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

type Descriptor = { id: string; type: string; transports?: string[] }

const decodeDescriptors = (list?: Descriptor[]) =>
  list?.map((d) => ({ ...d, id: fromB64url(d.id) })) as PublicKeyCredentialDescriptor[] | undefined

/** A cancelled or timed-out browser prompt, which the UI shows as a gentle note rather than an error. */
export class PasskeyCancelled extends Error {}

function rethrow(e: unknown): never {
  const name = (e as DOMException)?.name
  if (name === 'NotAllowedError' || name === 'AbortError') throw new PasskeyCancelled('The passkey prompt was closed.')
  if (name === 'InvalidStateError') throw new Error('This device already holds a passkey for your account.')
  throw e
}

export async function createPasskey(options: Record<string, any>) {
  const publicKey: PublicKeyCredentialCreationOptions = {
    ...(options as PublicKeyCredentialCreationOptions),
    challenge: fromB64url(options.challenge),
    user: { ...options.user, id: fromB64url(options.user.id) },
    excludeCredentials: decodeDescriptors(options.excludeCredentials),
  }
  let cred: PublicKeyCredential
  try {
    cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential
  } catch (e) {
    rethrow(e)
  }
  const res = cred.response as AuthenticatorAttestationResponse
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: toB64url(res.clientDataJSON),
      attestationObject: toB64url(res.attestationObject),
      transports: typeof res.getTransports === 'function' ? res.getTransports() : undefined,
    },
  }
}

export async function getPasskey(options: Record<string, any>) {
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...(options as PublicKeyCredentialRequestOptions),
    challenge: fromB64url(options.challenge),
    allowCredentials: decodeDescriptors(options.allowCredentials),
  }
  let cred: PublicKeyCredential
  try {
    cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential
  } catch (e) {
    rethrow(e)
  }
  const res = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: toB64url(res.clientDataJSON),
      authenticatorData: toB64url(res.authenticatorData),
      signature: toB64url(res.signature),
      userHandle: toB64url(res.userHandle),
    },
  }
}
