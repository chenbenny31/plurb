// Read-heavy traffic: index pages, a post, the forum and a thread.
//   k6 run -e BASE=http://127.0.0.1:8080 -e VUS=50 -e DURATION=1m loadtest/read.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE } from './lib.js';

export const options = {
  vus: Number(__ENV.VUS || 50),
  duration: __ENV.DURATION || '1m',
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{page:post}': ['p(95)<200'],
    'http_req_duration{page:thread}': ['p(95)<250'],
  },
};

export function setup() {
  // Discover a post and a thread to read.
  const index = http.get(`${BASE}/panorama`).body;
  const post = /href="(\/panorama\/[a-z0-9_]+\/[a-z0-9-]+)"/.exec(index);
  const forum = http.get(`${BASE}/forum?sort=new`).body;
  const thread = /href="(\/forum\/t\/\d+)"/.exec(forum);
  return { post: post && post[1], thread: thread && thread[1] };
}

export default function (d) {
  const pages = [
    ['index', '/panorama'],
    ['forum', '/forum'],
  ];
  if (d.post) pages.push(['post', d.post]);
  if (d.thread) pages.push(['thread', d.thread]);
  for (const [name, path] of pages) {
    const res = http.get(`${BASE}${path}`, { tags: { page: name } });
    check(res, { [`${name} 200`]: (r) => r.status === 200 });
  }
  sleep(1);
}
