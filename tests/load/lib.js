// Shared helpers of the load scenarios (NFR-10, WP-204).
import http from 'k6/http';
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

export function get(path, name) {
  const res = http.get(`${BASE_URL}/api/v1${path}`, params(name));
  check(res, { [`${name} 200`]: (r) => r.status === 200 });
  return res;
}

export function post(path, body, name, okStatus = [200, 202]) {
  const res = http.post(`${BASE_URL}/api/v1${path}`, JSON.stringify(body), params(name));
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
