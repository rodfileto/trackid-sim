// Concurrent users on the stress stack (docs/stress-test-plan.md in ../trackid, phase 2): a
// class's examiners working their analyses, a coordinator handing out the work, analysts on
// the intelligence pages and a few people in the fingerprint image lab, all at once. Run by
// scripts/stress/class.sh, which loads a world, creates the accounts and runs k6 in a container
// on the host's network; nothing leaves the machine.
//
// Environment: API (base URL), KEY (the class key file from `trackid-sim users`), LATENT (a
// fingerprint image for the lab), VUS (examiners at the hold, doubled in the spike), HOLD, RAMP,
// ANALYSTS, LAB, MAXPAIRS (pairs an examiner works per visit).

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

const API = __ENV.API || 'http://127.0.0.1:8182';
const key = JSON.parse(open(__ENV.KEY || '/stress/class-key.json'));
// The planting account is created inactive (`trackid-sim users`); it cannot log in.
const examiners = key.examiners.filter((e) => e.username !== key.planter);
const latent = __ENV.LATENT ? open(__ENV.LATENT, 'b') : null;

const VUS = parseInt(__ENV.VUS || '30', 10);
const RAMP = __ENV.RAMP || '2m';
const HOLD = __ENV.HOLD || '5m';
const ANALYSTS = parseInt(__ENV.ANALYSTS || '3', 10);
const LAB = latent ? parseInt(__ENV.LAB || '2', 10) : 0;
const MAXPAIRS = parseInt(__ENV.MAXPAIRS || '6', 10);

const seconds = (d) => {
  const m = /^(\d+)(s|m|h)$/.exec(d);
  if (!m) throw new Error(`duration ${d}: use s, m or h`);
  return parseInt(m[1], 10) * { s: 1, m: 60, h: 3600 }[m[2]];
};
// Ramp, hold, spike to twice the examiners, hold the spike, ramp down.
const TOTAL = `${seconds(RAMP) + seconds(HOLD) + 30 + 60 + 30}s`;

const scenarios = {
  examiners: {
    executor: 'ramping-vus', exec: 'examiner', startVUs: 1,
    stages: [
      { duration: RAMP, target: VUS },
      { duration: HOLD, target: VUS },
      { duration: '30s', target: VUS * 2 },
      { duration: '1m', target: VUS * 2 },
      { duration: '30s', target: 0 },
    ],
    gracefulRampDown: '30s',
  },
  coordinator: { executor: 'constant-vus', exec: 'coordinator', vus: 1, duration: TOTAL },
};
if (ANALYSTS > 0) scenarios.analysts = { executor: 'constant-vus', exec: 'analyst', vus: ANALYSTS, duration: TOTAL };
if (LAB > 0) scenarios.lab = { executor: 'constant-vus', exec: 'lab', vus: LAB, duration: TOTAL };

// Pass criteria proposed in the plan: reads p95 under 500 ms, writes under 2 s, no failures
// other than the 4xx a request can get by design (marked expected below). The lab's renders and
// the analysts' graphs are measured on their own, with no budget yet.
export const options = {
  scenarios,
  thresholds: {
    'http_req_duration{kind:read}': ['p(95)<500'],
    'http_req_duration{kind:write}': ['p(95)<2000'],
    http_req_failed: ['rate==0'],
    // No budget: listed so the summary breaks the times down by request.
    ...Object.fromEntries(['login', 'my_analyses', 'cases', 'case', 'worksheet', 'pair_image', 'save_draft', 'submit',
      'distribution_status', 'distribute', 'requests', 'clusters', 'clusters_graph', 'occurrence_map', 'overview_graph',
      'lab_upload', 'lab_estimate', 'lab_render'].map((n) => [`http_req_duration{name:${n}}`, ['max>=0']])),
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const submitted = new Counter('steps_submitted');
const refused = new Counter('submits_refused'); // 400/409: the step moved on or was incomplete
const idle = new Counter('examiner_idle'); // a visit with nothing to work on

const rand = (lo, hi) => lo + Math.random() * (hi - lo);
const think = (lo, hi) => sleep(rand(lo, hi));
const pick = (xs) => xs[Math.floor(Math.random() * xs.length)];

let token = null; // per VU
function login(account) {
  const r = http.post(`${API}/api/auth/login`, JSON.stringify({ staff_number: account.username, password: account.password }), {
    headers: { 'Content-Type': 'application/json' }, tags: { name: 'login', kind: 'write' },
  });
  check(report(r, 'login'), { 'logged in': (x) => x.status === 200 });
  return r.status === 200 ? r.json('token') : null;
}

function params(name, kind, expected) {
  const p = { headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, tags: { name, kind } };
  if (expected) p.responseCallback = http.expectedStatuses({ min: 200, max: 299 }, ...expected);
  return p;
}
// Every failure k6 counts (outside a request's expected statuses) is logged, so a run says which
// requests failed and why; the API itself logs no requests.
let logged = 0; // per VU
let refusedLogged = 0;
function report(r, name, expected) {
  const ok = (r.status >= 200 && r.status < 300) || (expected || []).includes(r.status);
  if (!ok && logged++ < 20) console.error(`FAILED ${name} ${r.request.method} ${r.url.replace(API, '')} -> ${r.status} ${String(r.body || r.error).slice(0, 200)}`);
  return r;
}
const get = (path, name, kind = 'read') => report(http.get(`${API}${path}`, params(name, kind)), name);
const send = (method, path, body, name, expected) =>
  report(http.request(method, `${API}${path}`, JSON.stringify(body), params(name, 'write', expected)), name, expected);
const json = (r) => {
  try { return r.json(); } catch (e) { return null; }
};

// __VU numbers virtual users across every scenario, so class.sh creates one account per virtual
// user that can exist at once, and no two of them share one.
const accountOf = () => examiners[(__VU - 1) % examiners.length];

export function examiner() {
  const account = accountOf();
  if (!token && !(token = login(account))) return think(5, 10);

  const mine = json(get('/api/me/analyses', 'my_analyses')) || [];
  // At REPORT the examiner's turn is the report, which this mix does not write yet.
  const turn = mine.filter((a) => a.yourTurn && a.stage !== 'REPORT');
  if (turn.length === 0) {
    idle.add(1);
    get('/api/cases?page=1&page_size=25', 'cases');
    return think(3, 8);
  }
  const a = pick(turn);
  const kase = `/api/cases/${encodeURIComponent(a.caseId)}`;
  const base = `${kase}/analyses/${a.analysisId}`;
  if (!a.blind) get(kase, 'case');
  const ws = json(get(`${base}/pairs`, 'worksheet'));
  if (!ws) return think(2, 4);
  think(1, 3);

  const todo = (ws.pairs || []).filter((p) => !p.draft);
  for (const p of todo.slice(0, MAXPAIRS)) {
    get(`${base}/pairs/image?feature=${encodeURIComponent(p.featureA)}`, 'pair_image');
    get(`${base}/pairs/image?feature=${encodeURIComponent(p.featureB)}`, 'pair_image');
    think(2, 6); // looking at the pair
    send('PUT', `${base}/decisions`, { featureA: p.featureA, featureB: p.featureB, decision: pick(['POSITIVE', 'NEGATIVE', 'INCONCLUSIVE']) },
      'save_draft', [400, 409]);
  }
  if (todo.length <= MAXPAIRS) {
    const r = send('POST', `${base}/submit`, {}, 'submit', [400, 409]);
    if (r.status === 200) submitted.add(1);
    else {
      refused.add(1);
      if (refusedLogged++ < 3) console.warn(`REFUSED submit ${r.status} ${String(r.body).slice(0, 200)}`);
    }
  }
  think(1, 3);
}

export function coordinator() {
  if (!token && !(token = login(key.instructor))) return think(5, 10);
  get('/api/admin/distribution', 'distribution_status');
  send('POST', '/api/admin/distribution', { count: 20 }, 'distribute');
  get('/api/admin/requests', 'requests');
  think(20, 40);
}

export function analyst() {
  if (!token && !(token = login(key.instructor))) return think(5, 10);
  get('/api/clusters?page=1&page_size=25', 'clusters', 'analysis');
  think(2, 5);
  get('/api/clusters/graph', 'clusters_graph', 'analysis');
  think(2, 5);
  get('/api/occurrences/map', 'occurrence_map', 'analysis');
  think(2, 5);
  get('/api/overview/graph', 'overview_graph', 'analysis');
  think(3, 8);
}

let source = null; // per VU: the lab keeps uploaded sources in memory, by their SHA-256
export function lab() {
  const account = accountOf();
  if (!token && !(token = login(account))) return think(5, 10);
  if (!source) {
    const r = http.post(`${API}/api/imageproc/sources`, { file: http.file(latent, 'latent.png', 'image/png') }, {
      headers: { Authorization: `Bearer ${token}` }, tags: { name: 'lab_upload', kind: 'lab' },
    });
    source = json(report(r, 'lab_upload'));
    if (!source) return think(5, 10);
  }
  const crop = { x1: 0, y1: 0, x2: source.width, y2: source.height };
  const est = json(report(http.post(`${API}/api/imageproc/estimate`, JSON.stringify({ sourceId: source.id, crop, channel: 'luminance' }),
    params('lab_estimate', 'lab')), 'lab_estimate'));
  const period = (est && est.periodPx) || 9;
  think(2, 5);
  for (let i = 0; i < 3; i++) { // an examiner tries a few settings
    const recipe = {
      source: { method: 'ESTIMATED', ridgePeriodPx: period, ridgeDistanceMm: 0.48 },
      clahe: { tiles: pick([4, 8]), clipLimit: pick([2, 3]) },
      gabor: i === 2 ? { orientations: 8 } : undefined,
    };
    report(http.post(`${API}/api/imageproc/render`, JSON.stringify({ sourceId: source.id, recipe }), params('lab_render', 'lab')), 'lab_render');
    think(3, 8);
  }
}

export function handleSummary(data) {
  const out = __ENV.SUMMARY || '/out/summary.json';
  return { [out]: JSON.stringify(data, null, 2), stdout: textSummary(data) };
}

// A short text summary: k6's own needs a remote module, and nothing is fetched from outside.
function textSummary(data) {
  const m = data.metrics;
  const lines = ['', '== class stress =='];
  for (const [name, v] of Object.entries(m)) {
    if (!name.startsWith('http_req_duration{') && name !== 'http_req_duration') continue;
    const s = v.values;
    lines.push(`${name.padEnd(46)} p50 ${s.med.toFixed(0)}ms  p95 ${s['p(95)'].toFixed(0)}ms  p99 ${s['p(99)'].toFixed(0)}ms  max ${s.max.toFixed(0)}ms`);
  }
  for (const name of ['http_reqs', 'http_req_failed', 'steps_submitted', 'submits_refused', 'examiner_idle', 'vus_max']) {
    if (m[name]) lines.push(`${name.padEnd(46)} ${JSON.stringify(m[name].values)}`);
  }
  for (const [name, v] of Object.entries(m)) {
    if (v.thresholds) for (const [t, r] of Object.entries(v.thresholds)) lines.push(`threshold ${name} ${t}: ${r.ok ? 'ok' : 'FAILED'}`);
  }
  return lines.join('\n') + '\n';
}
