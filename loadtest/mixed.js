// Mixed forum traffic: every VU signs up, then reads threads, comments and
// votes. Votes from many VUs land on the same hot threads, exercising the
// row locks in the vote transaction under contention.
//
// Start the server with open sign-up and relaxed limits:
//   PLURB_SIGNUP=open PLURB_RATE_LIMIT_SCALE=1000 plurb serve
//   k6 run -e BASE=http://127.0.0.1:8080 -e VUS=30 -e DURATION=1m loadtest/mixed.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { BASE, postParams, signup, idFromLocation } from './lib.js';

export const options = {
  vus: Number(__ENV.VUS || 30),
  duration: __ENV.DURATION || '1m',
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    'http_req_duration{op:vote}': ['p(95)<300'],
    'http_req_duration{op:comment}': ['p(95)<400'],
  },
};

const RUN = Date.now().toString(36).slice(-5);

// setup seeds a handful of shared threads that every VU will hammer.
export function setup() {
  signup(`seed_${RUN}`, 'load-test-password');
  const threads = [];
  for (let i = 0; i < 5; i++) {
    const res = http.post(`${BASE}/forum/new`, { title: `load thread ${i} ${RUN}`, body: 'seeded by k6' }, postParams);
    const id = idFromLocation(res);
    if (id) threads.push(id);
  }
  check(threads, { 'seeded threads': (t) => t.length === 5 });
  return { threads };
}

let signedIn = false;

export default function (d) {
  if (!signedIn) {
    signedIn = signup(`vu${exec.vu.idInTest}_${RUN}`, 'load-test-password');
  }
  const id = d.threads[Math.floor(Math.random() * d.threads.length)];

  check(http.get(`${BASE}/forum/t/${id}`, { tags: { op: 'read' } }), { 'thread 200': (r) => r.status === 200 });

  // Flip between up and retract so votes keep producing ledger entries.
  const value = Math.random() < 0.7 ? '1' : '0';
  const vote = http.post(`${BASE}/forum/vote`, { type: 'thread', id, value, thread: id },
    { headers: { Origin: BASE, Accept: 'application/json' }, tags: { op: 'vote' } });
  check(vote, { 'vote ok': (r) => r.status === 200 });

  if (Math.random() < 0.3) {
    const c = http.post(`${BASE}/forum/t/${id}/comments`, { body: `k6 says hi (${exec.vu.idInTest})` },
      Object.assign({ tags: { op: 'comment' } }, postParams));
    check(c, { 'comment 303': (r) => r.status === 303 });
  }
  sleep(0.5);
}

// teardown checks the invariant the vote transaction protects:
// each thread's score equals the sum of its votes' weights.
export function teardown(d) {
  // Scores are only visible as rendered text; this run logs them for a
  // manual comparison with:  SELECT id, score, (SELECT sum(value*weight)
  //   FROM votes WHERE target_type='thread' AND target_id=t.id) FROM threads t;
  for (const id of d.threads) {
    const body = http.get(`${BASE}/forum/t/${id}`).body;
    const m = /class="vote-score">([^<]+)</.exec(body);
    console.log(`thread ${id} score ${m ? m[1] : '?'}`);
  }
}
