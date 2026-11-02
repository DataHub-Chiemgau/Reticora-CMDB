/**
 * Tests for the ALTCHA proof of work of the signup (WP-112): the solution
 * the server verifies, and the readable refusal outside a secure context.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { InsecureContextError } from '../../auth/oidc';
import { solveAltcha, type AltchaChallenge } from './altcha';

const originalCryptoDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');

afterEach(() => {
  if (originalCryptoDescriptor) {
    Object.defineProperty(globalThis, 'crypto', originalCryptoDescriptor);
  }
});

async function challengeFor(salt: string, secret: number): Promise<AltchaChallenge> {
  const digest = await crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(`${salt}${secret}`),
  );
  const challenge = Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join(
    '',
  );
  return { algorithm: 'SHA-256', challenge, maxnumber: 20, salt, signature: 'sig' };
}

describe('solveAltcha', () => {
  it('finds the secret number and returns the payload the server verifies', async () => {
    const challenge = await challengeFor('salt?expires=9999999999', 11);
    const payload = JSON.parse(atob(await solveAltcha(challenge)));
    expect(payload).toEqual({
      algorithm: 'SHA-256',
      challenge: challenge.challenge,
      number: 11,
      salt: 'salt?expires=9999999999',
      signature: 'sig',
    });
  });

  it('gives up beyond maxnumber', async () => {
    const challenge = await challengeFor('salt', 30);
    await expect(solveAltcha(challenge)).rejects.toThrow('no ALTCHA solution');
  });

  it('refuses with InsecureContextError when the Web Crypto digest is unavailable', async () => {
    const challenge = await challengeFor('salt', 3);
    // On plain HTTP (not localhost) browsers keep crypto.getRandomValues but
    // leave crypto.subtle undefined: the page shows auth.secureContextRequired
    // instead of the TypeError of an undefined digest.
    Object.defineProperty(globalThis, 'crypto', {
      value: { getRandomValues: (values: Uint8Array) => values },
      configurable: true,
    });
    await expect(solveAltcha(challenge)).rejects.toThrow(InsecureContextError);

    Object.defineProperty(globalThis, 'crypto', { value: undefined, configurable: true });
    await expect(solveAltcha(challenge)).rejects.toThrow(InsecureContextError);
  });
});
