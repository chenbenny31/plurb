// Shared helpers for plurb k6 scripts.
import http from 'k6/http';
import { check } from 'k6';

export const BASE = __ENV.BASE || 'http://127.0.0.1:8080';

// The app's CSRF guard checks Origin on POSTs, like a browser would send.
export const postParams = { headers: { Origin: BASE }, redirects: 0 };

// signup creates (or logs into) an account; each VU keeps its own cookie jar.
export function signup(username, password) {
  let res = http.post(`${BASE}/signup`, {
    username, password, password_confirm: password,
  }, postParams);
  if (res.status !== 303) {
    res = http.post(`${BASE}/login`, { username, password }, postParams);
  }
  check(res, { 'signed in': (r) => r.status === 303 });
  return res.status === 303;
}

// idFromLocation pulls the numeric id out of a redirect like /forum/t/12.
export function idFromLocation(res) {
  const m = /\/forum\/t\/(\d+)/.exec(res.headers.Location || '');
  return m ? m[1] : null;
}
