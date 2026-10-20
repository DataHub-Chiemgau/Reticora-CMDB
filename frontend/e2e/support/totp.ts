import { createHmac } from 'node:crypto';
import { mkdirSync, readFileSync, rmdirSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Page } from '@playwright/test';

// WP-357 (AUT-09, CH26): the realm asks every holder of the realm role
// mfa_required – the first org_admin of a signup among them – for a TOTP
// code (browser flow browser-mfa) and lets a new account set it up at its
// first login (required action CONFIGURE_TOTP). The browser suites against a
// real Keycloak (e2e/onboarding.spec.ts, e2e-real/) sign in like a person
// with an authenticator app: the secret is read from the setup page and kept
// per account in a file, so every test, worker and browser context of a run
// can compute the codes (RFC 6238 with the realm's OTP policy: HmacSHA1,
// 6 digits, 30 seconds). Keycloak refuses a code that was already used (and
// counts it as a failed login), so a code is sent at most once: the next
// login waits for the next time step.

const PERIOD_SECONDS = 30;
const DIGITS = 6;
const STORE = process.env.E2E_TOTP_DIR || join(tmpdir(), 'reticora-e2e-totp');

interface Entry {
  /** The secret as Keycloak's setup page holds it (its UTF-8 bytes are the HMAC key). */
  secret: string;
  /** The last time step a code was sent for. */
  lastStep: number;
}

/** totp computes the RFC 6238 code of secret for the given time step. */
export function totp(secret: string, step: number): string {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(step));
  const mac = createHmac('sha1', Buffer.from(secret, 'utf8')).update(counter).digest();
  const offset = mac[mac.length - 1]! & 0x0f;
  const value = mac.readUInt32BE(offset) & 0x7fffffff;
  return String(value % 10 ** DIGITS).padStart(DIGITS, '0');
}

function entryFile(email: string): string {
  return join(STORE, `${encodeURIComponent(email.toLowerCase())}.json`);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** withLock serializes the code choice for one account across workers. */
async function withLock<T>(email: string, fn: () => Promise<T>): Promise<T> {
  mkdirSync(STORE, { recursive: true, mode: 0o700 });
  const lock = `${entryFile(email)}.lock`;
  for (;;) {
    try {
      mkdirSync(lock);
      break;
    } catch {
      // A lock older than two waits for a time step is left over from a
      // killed worker.
      try {
        if (Date.now() - statSync(lock).mtimeMs > 4 * PERIOD_SECONDS * 1000) rmdirSync(lock);
      } catch {
        // released in the meantime
      }
      await sleep(200);
    }
  }
  try {
    return await fn();
  } finally {
    rmdirSync(lock);
  }
}

function readEntry(email: string): Entry | undefined {
  try {
    return JSON.parse(readFileSync(entryFile(email), 'utf8')) as Entry;
  } catch {
    return undefined;
  }
}

function writeEntry(email: string, entry: Entry) {
  writeFileSync(entryFile(email), JSON.stringify(entry), { mode: 0o600 });
}

/**
 * nextCode returns a code for the account that was never sent before: the
 * code of the current time step, or – when that one is used up – the code of
 * the next step after waiting for it.
 */
async function nextCode(email: string, secret?: string): Promise<string> {
  return withLock(email, async () => {
    const entry = readEntry(email) ?? (secret ? { secret, lastStep: 0 } : undefined);
    if (!entry) throw new Error(`no TOTP secret stored for ${email} in ${STORE}`);
    if (secret) entry.secret = secret;
    let step = Math.floor(Date.now() / 1000 / PERIOD_SECONDS);
    if (step <= entry.lastStep) {
      step = entry.lastStep + 1;
      await sleep(step * PERIOD_SECONDS * 1000 - Date.now() + 250);
    }
    entry.lastStep = step;
    writeEntry(email, entry);
    return totp(entry.secret, step);
  });
}

/**
 * keycloakOtp completes the second factor after the password form: it sets
 * up OTP when Keycloak asks for it (CONFIGURE_TOTP) or enters the code on
 * the OTP form. It returns without action when Keycloak asks for neither
 * (account without mfa_required) and the login has reached landing or another
 * page; the caller's own checks take over from there.
 */
export async function keycloakOtp(page: Page, email: string, landing: RegExp) {
  const setup = page.locator('#kc-totp-settings-form');
  const form = page.locator('#kc-otp-login-form');
  await Promise.race([
    page.waitForURL(landing, { timeout: 60_000 }),
    setup.waitFor({ state: 'visible', timeout: 60_000 }),
    form.waitFor({ state: 'visible', timeout: 60_000 }),
    page.locator('#lastName').waitFor({ state: 'visible', timeout: 60_000 }),
  ]).catch(() => undefined);
  let step: { code: string; input: string; submit: string } | undefined;
  if (await setup.isVisible().catch(() => false)) {
    const secret = await page.locator('#totpSecret').inputValue();
    step = { code: await nextCode(email, secret), input: '#totp', submit: '#saveTOTPBtn' };
  } else if (await form.isVisible().catch(() => false)) {
    step = { code: await nextCode(email), input: '#otp', submit: '#kc-login' };
  }
  if (!step) return;
  await page.locator(step.input).fill(step.code);
  // Every Keycloak step posts to a new login-actions URL: wait until the
  // browser has left the OTP page before the caller looks at the next one.
  const before = page.url();
  await page.locator(step.submit).click();
  await page.waitForURL((url) => url.href !== before, { timeout: 30_000 });
}
