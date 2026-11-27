// k6 stress test for DBMP. Run (from the repository root, stack running):
//
//   docker run --rm --network k03-04_wbd_default -v "$PWD/loadtest:/scripts" \
//     grafana/k6:0.54.0 run /scripts/stress.js
//
// Two scenarios run at the same time:
//   api  - logged-in users browse the API (databases, tables, rows, migrations,
//          history). Load rises in steps above the normal load (10 VUs), peaks,
//          then drops back to watch recovery.
//   jobs - users publish approved migrations to the queue on two databases
//          (same and different database) and follow each job until the worker
//          finishes. The response to "execute" only means "queued"; a job counts
//          as successful only when the worker reports success.
import http from 'k6/http'
import { check, sleep } from 'k6'
import { Counter, Rate, Trend } from 'k6/metrics'

const BASE = __ENV.BASE_URL || 'http://app:8080'
const ADMIN = { username: __ENV.ADMIN_USERNAME || 'admin', password: __ENV.ADMIN_PASSWORD || 'admin12345' }
const TARGET_HOST = __ENV.TARGET_HOST || 'sample-db'
const TARGET_PORT = Number(__ENV.TARGET_PORT || 5432)

const jobE2E = new Trend('job_e2e_ms', true)           // execute -> worker finished
const jobQueueWait = new Trend('job_queue_wait_ms', true) // enqueued -> claimed by a worker
const jobExec = new Trend('job_exec_ms', true)          // claimed -> finished
const jobSuccess = new Rate('job_success')
const jobsDone = new Counter('jobs_completed')

export const options = {
  noCookiesReset: true, // keep the session cookie between iterations, like a browser
  scenarios: {
    api: {
      executor: 'ramping-vus', exec: 'browse', startVUs: 0,
      stages: [
        { duration: '30s', target: 10 },  // normal load
        { duration: '30s', target: 10 },
        { duration: '30s', target: 30 },  // 3x normal
        { duration: '30s', target: 30 },
        { duration: '30s', target: 60 },  // 6x normal (stress)
        { duration: '30s', target: 60 },
        { duration: '30s', target: 10 },  // back to normal: recovery
        { duration: '30s', target: 10 },
        { duration: '10s', target: 0 },
      ],
      tags: { scenario: 'api' },
    },
    jobs: {
      executor: 'ramping-vus', exec: 'migrate', startVUs: 0,
      stages: [
        { duration: '30s', target: 2 },
        { duration: '60s', target: 2 },
        { duration: '30s', target: 6 },
        { duration: '60s', target: 6 },
        { duration: '30s', target: 2 },
        { duration: '40s', target: 0 },
      ],
      tags: { scenario: 'jobs' },
    },
  },
  thresholds: {
    'http_req_duration{scenario:api}': ['p(95)<500', 'p(50)<150'],
    'http_req_failed': ['rate<0.01'],
    'job_success': ['rate>0.95'],
    'job_e2e_ms': ['p(95)<30000'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max'],
}

let csrf = null

function json(method, path, body, token) {
  const params = { headers: { 'Content-Type': 'application/json' }, tags: { name: path.replace(/\d+/g, ':id') } }
  if (token) params.headers['X-CSRF-Token'] = token
  const r = http.request(method, BASE + path, body === undefined ? null : JSON.stringify(body), params)
  if (r.status === 401 && path !== '/api/login') csrf = null // session ended: the VU logs in again
  return r
}

function login() {
  const r = json('POST', '/api/login', ADMIN)
  check(r, { 'login 200': x => x.status === 200 })
  return r.json('csrf')
}

// setup prepares two connections that only the k6 group can reach, so the
// admin (leader of that group) is the only approver needed.
export function setup() {
  const csrf = login()
  let groups = json('GET', '/api/admin/groups').json()
  let g = groups.find(x => x.name === 'k6')
  if (!g) g = { id: json('POST', '/api/admin/groups', { name: 'k6' }, csrf).json('id') }
  const me = json('GET', '/api/session').json('user')
  json('POST', `/api/admin/groups/${g.id}/members`, { user_id: me.user_id }, csrf)
  json('PUT', `/api/admin/groups/${g.id}/leader`, { user_id: me.user_id }, csrf)

  const dbs = json('GET', '/api/admin/databases').json()
  const ids = []
  for (const [name, dbname] of [['k6-lab', 'lab'], ['k6-shop', 'shop']]) {
    let d = dbs.find(x => x.name === name)
    if (!d) {
      d = { id: json('POST', '/api/admin/databases', {
        name, host: TARGET_HOST, port: TARGET_PORT, dbname, username: 'sample', password: 'sample', sslmode: 'disable',
      }, csrf).json('id') }
    }
    json('PUT', `/api/admin/groups/${g.id}/grants`, { database_id: d.id, level: 'edit' }, csrf)
    ids.push(d.id)
  }
  // The grant changes rotated the session; the VUs log in themselves.
  return { dbs: ids, tables: ['events', 'customers'] }
}


export function browse(data) {
  if (!csrf) csrf = login()
  const i = __ITER % data.dbs.length
  const db = data.dbs[i]
  const res = [
    json('GET', '/api/databases'),
    json('GET', `/api/db/${db}/tables`),
    json('GET', `/api/db/${db}/tables/public/${data.tables[i]}/rows?offset=0`),
    json('GET', `/api/db/${db}/migrations`),
    json('GET', `/api/db/${db}/history?hide_rollback=1`),
  ]
  for (const r of res) check(r, { 'api 200': x => x.status === 200 })
  sleep(1)
}

export function migrate(data) {
  if (!csrf) csrf = login()
  // VUs alternate between the two databases: jobs on the same database queue
  // behind its lock, jobs on different databases run in parallel.
  const db = data.dbs[(__VU + __ITER) % data.dbs.length]
  const tag = `k6-${__VU}-${__ITER}-${Date.now()}`
  const m = json('POST', `/api/db/${db}/migrations`, {
    name: tag, mode: 'normal',
    up_sql: `CREATE TABLE IF NOT EXISTS events (id bigserial PRIMARY KEY, kind text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());\nINSERT INTO events (kind) VALUES ('${tag}');`,
    down_sql: `DELETE FROM events WHERE kind = '${tag}';`,
  }, csrf)
  if (!check(m, { 'migration created': x => x.status === 201 })) return sleep(1)
  const req = json('POST', `/api/db/${db}/migrations/${m.json('id')}/requests`, { action: 'apply', note: 'k6' }, csrf)
  if (!check(req, { 'request submitted': x => x.status === 201 })) return sleep(1)
  const rid = req.json('id')
  check(json('POST', `/api/db/${db}/requests/${rid}/approve`, { comment: 'k6' }, csrf), { 'approved': x => x.status === 200 })

  const start = Date.now()
  const ex = json('POST', `/api/db/${db}/requests/${rid}/execute`, {}, csrf)
  if (!check(ex, { 'job queued (202)': x => x.status === 202 })) return sleep(1)
  const jobID = ex.json('job_id')

  for (let i = 0; i < 120; i++) {
    sleep(0.5)
    const j = json('GET', `/api/db/${db}/jobs/${jobID}`)
    if (j.status !== 200) continue
    const job = j.json('job')
    if (job.status === 'done' || job.status === 'failed') {
      const ok = j.json('request.status') === 'success'
      jobSuccess.add(ok)
      jobsDone.add(1)
      jobE2E.add(Date.now() - start)
      const enq = Date.parse(job.enqueued_at), st = Date.parse(job.started_at), fin = Date.parse(job.finished_at)
      if (st) jobQueueWait.add(st - enq)
      if (st && fin) jobExec.add(fin - st)
      return
    }
  }
  jobSuccess.add(false) // did not finish in 60 s
}

export function handleSummary(data) {
  return {
    '/scripts/results/summary.json': JSON.stringify(data, null, 2),
    stdout: textSummary(data),
  }
}

function textSummary(data) {
  const m = data.metrics
  const t = (name, tag = '') => {
    const v = m[name + tag]?.values
    return v ? `avg=${v.avg?.toFixed(1)} p50=${v.med?.toFixed(1)} p95=${v['p(95)']?.toFixed(1)} max=${v.max?.toFixed(1)}` : '-'
  }
  const lines = [
    '',
    `http_req_duration{scenario:api} ms: ${t('http_req_duration', '{scenario:api}')}`,
    `http_reqs: ${m.http_reqs.values.count} (${m.http_reqs.values.rate.toFixed(1)}/s)`,
    `http_req_failed: ${(m.http_req_failed.values.rate * 100).toFixed(2)}%`,
    `jobs_completed: ${m.jobs_completed?.values.count ?? 0} (${(m.jobs_completed?.values.rate ?? 0).toFixed(2)}/s)`,
    `job_success: ${((m.job_success?.values.rate ?? 0) * 100).toFixed(1)}%`,
    `job_queue_wait_ms: ${t('job_queue_wait_ms')}`,
    `job_exec_ms: ${t('job_exec_ms')}`,
    `job_e2e_ms: ${t('job_e2e_ms')}`,
    `thresholds: ${Object.entries(m).filter(([, v]) => v.thresholds).map(([k, v]) =>
      `${k} ${Object.values(v.thresholds).every(x => x.ok) ? 'OK' : 'FAILED'}`).join(', ')}`,
    '',
  ]
  return lines.join('\n')
}
