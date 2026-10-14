/**
 * solveAltcha finds the proof of work of an ALTCHA challenge (WP-112): the
 * number n with SHA-256(salt + n) = challenge, returned as the base64 JSON
 * payload the signup endpoint verifies.
 */
export interface AltchaChallenge {
  algorithm: string;
  challenge: string;
  maxnumber: number;
  salt: string;
  signature: string;
}

async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

export async function solveAltcha(c: AltchaChallenge): Promise<string> {
  for (let n = 0; n <= c.maxnumber; n++) {
    if ((await sha256Hex(c.salt + n)) === c.challenge) {
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
