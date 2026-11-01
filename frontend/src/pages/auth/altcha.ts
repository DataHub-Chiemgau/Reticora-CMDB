/**
 * solveAltcha finds the proof of work of an ALTCHA challenge (WP-112): the
 * number n with SHA-256(salt + n) = challenge, returned as the base64 JSON
 * payload the signup endpoint verifies.
 */
import { InsecureContextError } from '../../auth/oidc';

export interface AltchaChallenge {
  algorithm: string;
  challenge: string;
  maxnumber: number;
  salt: string;
  signature: string;
}

/**
 * subtleCrypto is the Web Crypto digest API. Browsers expose it only in a
 * secure context (HTTPS or localhost); on plain HTTP crypto.subtle is
 * undefined, which the sign-in reports the same way (auth/oidc.ts).
 */
function subtleCrypto(): SubtleCrypto {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle || typeof subtle.digest !== 'function') {
    throw new InsecureContextError();
  }
  return subtle;
}

async function sha256Hex(subtle: SubtleCrypto, value: string): Promise<string> {
  const digest = await subtle.digest('SHA-256', new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

export async function solveAltcha(c: AltchaChallenge): Promise<string> {
  const subtle = subtleCrypto();
  for (let n = 0; n <= c.maxnumber; n++) {
    if ((await sha256Hex(subtle, c.salt + n)) === c.challenge) {
      return btoa(
        JSON.stringify({
          algorithm: c.algorithm,
          challenge: c.challenge,
          number: n,
          salt: c.salt,
          signature: c.signature,
        }),
      );
    }
  }
  throw new Error('no ALTCHA solution');
}
