// Shared helpers of the load scenarios (NFR-10, WP-204, WP-437).
import http from 'k6/http';
import crypto from 'k6/crypto';
import { check, fail } from 'k6';

export const BASE_URL = (__ENV.BASE_URL || 'http://localhost:8080').replace(/\/$/, '');
const API_KEY = __ENV.API_KEY || '';

export function params(name, extra = {}) {
  if (!API_KEY) {
    fail('API_KEY is required (cmd/seed -api-key-file)');
  }
  return {
    headers: { 'X-API-Key': API_KEY, 'Content-Type': 'application/json', ...(extra.headers || {}) },
    tags: { name },
    timeout: extra.timeout || '60s',
  };
}

// failed logs a request k6 counts as failed in http_req_failed (status 0,
// below 200 or from 400 on) on the console, with its name tag, status,
// method, path and the start of the body: load.yml writes the console to
// console-<job>.log and lists the failed requests per name and status in
// the job summary (tests/load/failed-requests.sh, F-388). It returns res.
export function failed(res, name) {
  if (res.status >= 200 && res.status < 400) {
    return res;
  }
  // One line without quotes or backslashes, which the log format escapes.
  const body = String(res.body || res.error || '').slice(0, 160).replace(/\s+/g, ' ').replace(/["\\]/g, "'");
  const path = String(res.url || '').replace(BASE_URL, '');
  console.warn(`failed request name=${name} status=${res.status} ${res.request ? res.request.method : ''} ${path} ${body}`);
  return res;
}

export function get(path, name) {
  const res = failed(http.get(`${BASE_URL}/api/v1${path}`, params(name)), name);
  check(res, { [`${name} 200`]: (r) => r.status === 200 });
  return res;
}

export function post(path, body, name, okStatus = [200, 202]) {
  const res = failed(http.post(`${BASE_URL}/api/v1${path}`, JSON.stringify(body), params(name)), name);
  check(res, { [`${name} ${okStatus.join('/')}`]: (r) => okStatus.includes(r.status) });
  return res;
}

// firstID returns the id of the first CI a list query finds.
export function firstID(query) {
  const res = get(`/cis?limit=1&${query}`, 'setup');
  const data = res.json('data');
  if (!data || data.length === 0) {
    fail(`no CI for ${query}; seed the database first (make seed SEED_OBJECTS=10000)`);
  }
  return data[0].id;
}

// total returns the total of a CI list query (-1 on an error).
export function total(query, name = 'count') {
  const res = failed(http.get(`${BASE_URL}/api/v1/cis?limit=1${query ? `&${query}` : ''}`, params(name)), name);
  return res.status === 200 ? Number(res.json('total')) : -1;
}

// The demo organization of backend/cmd/seed (-prefix empty): its slug and
// the second octet of its management network.
const SEED_PREFIX = __ENV.SEED_PREFIX || 'demo';
const SEED_OCTET = Number(__ENV.SEED_OCTET || 10);

const hex2 = (v) => (v & 255).toString(16).padStart(2, '0');

// seedDevice returns CI n of the demo organization as cmd/seed derives it:
// 1-2 switches, 3-4 PDUs, the rest servers; serial number, management IP
// and the MAC of eth0 from md5 of the seed key, like the seed's SQL.
export function seedDevice(n) {
  const kind = n <= 2 ? ['sw', 'switch', 'RX-48'] : n <= 4 ? ['pdu', 'pdu', 'PX-16'] : ['srv', 'server', 'R-1U'];
  const mac = crypto.md5(`${SEED_PREFIX}/mac/${n}`, 'hex');
  return {
    n,
    name: `${kind[0]}-${String(n).padStart(5, '0')}`,
    type: kind[1],
    model: kind[2],
    serial: `SN${crypto.md5(`${SEED_PREFIX}/serial/${n}`, 'hex').slice(0, 12).toUpperCase()}`,
    ip: `10.${SEED_OCTET}.${Math.floor(n / 250)}.${(n % 250) + 1}`,
    mac: `02:${[0, 2, 4, 6, 8].map((i) => mac.slice(i, i + 2)).join(':')}`,
    // Globally administered MACs the seed does not hold (REC-02 ignores
    // locally administered ones such as 02:…): the chassis MAC and eth1.
    chassisMAC: `ac:1f:6b:00:${hex2(n >> 8)}:${hex2(n)}`,
    eth1MAC: `ac:1f:6b:01:${hex2(n >> 8)}:${hex2(n)}`,
  };
}

// uuid returns a random version 4 UUID (record ids of the ingest).
export function uuid() {
  const hex = [];
  for (let i = 0; i < 16; i++) {
    hex.push(Math.floor(Math.random() * 256));
  }
  hex[6] = (hex[6] & 0x0f) | 0x40;
  hex[8] = (hex[8] & 0x3f) | 0x80;
  const s = hex.map((b) => b.toString(16).padStart(2, '0')).join('');
  return `${s.slice(0, 8)}-${s.slice(8, 12)}-${s.slice(12, 16)}-${s.slice(16, 20)}-${s.slice(20)}`;
}
