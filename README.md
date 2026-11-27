# DBMP — Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026 — **Milestone 2**.

Platform untuk mengatur akses ke database PostgreSQL bersama, menjelajahi schema dan data, serta menjalankan
perubahan schema/data lewat **migration** yang divalidasi, di-review, disetujui leader setiap tim, lalu dijalankan
**asynchronous** oleh worker. Migration dapat di-rollback dan di-forward dengan riwayat yang tetap utuh.

- **Backend & worker:** Go (`net/http`, `pgx/v5`, `log/slog`, OpenTelemetry, Prometheus client)
- **Frontend:** SPA React + Vite (`web/`), di-embed ke binary Go dan berkomunikasi lewat JSON API
- **Database platform & target:** PostgreSQL; queue juga disimpan di database platform

## Menjalankan

```bash
docker compose up -d --build
```

Buka <http://localhost:8080>, login `admin` / `admin12345`. Compose menjalankan `platform-db`, `sample-db`
(database `shop` dan `lab`), `app` (API + SPA), dan **2 worker** (`docker compose up -d --scale worker=4` untuk lebih).

Mencoba database contoh:

1. **Koneksi → + Koneksi baru**: host `sample-db`, port `5432`, database `shop`, user `sample`, password `sample`.
2. **Users**: buat user lain bila perlu. **Groups**: buat group, tambahkan anggota, pilih **leader**, beri akses database.
3. Buka database → **Migration → + Migration baru** → simpan → **Ajukan Apply** → leader menyetujui di
   **Dashboard** → **Jalankan** → pantau progres di halaman pengajuan.

> Platform Admin bukan tingkat akses database. Admin juga harus masuk group untuk melihat isi database.
> `sample-db` hanya diisi saat volume baru dibuat. Untuk volume lama jalankan
> `docker compose exec -T sample-db psql -U sample -d shop < deploy/sample-db/02_big_orders.sql` (dan `03_lab.sql`).

Development tanpa container app:

```bash
docker compose up -d platform-db sample-db
cd web && npm install && npm run build && cd ..          # SPA → web/dist (di-embed oleh Go)
export DATABASE_URL="postgres://dbmp:dbmp@localhost:15432/dbmp?sslmode=disable"
export APP_SECRET_KEY="ganti-dengan-rahasia-panjang" ADMIN_USERNAME=admin ADMIN_PASSWORD=admin12345
go run ./cmd/server &      # API + SPA di :8080
go run ./cmd/worker        # jalankan beberapa kali untuk beberapa worker (METRICS_ADDR berbeda)
# opsional: cd web && npm run dev  (Vite :5173, proxy /api ke :8080)
```

Environment variable: `DATABASE_URL` (wajib), `APP_SECRET_KEY` (wajib, ≥16 karakter), `APP_ADDR` (default `:8080`),
`METRICS_ADDR` (endpoint Prometheus, mis. `:9090`), `COOKIE_SECURE` (`true` di HTTPS), `ADMIN_USERNAME`/`ADMIN_PASSWORD`
(admin pertama), `LOG_LEVEL` (`debug` juga mencatat query database platform), `OTEL_TRACES_EXPORTER` (`otlp`/`console`/`none`),
`OTEL_LOGS_EXPORTER` (`otlp`/`none`), `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Arsitektur

```
SPA (React) ──JSON API──▶ server (Go) ──▶ platform DB: users, groups, migrations, change_requests,
                              │                       approvals, jobs (queue), executions (history)
                              │ publish job (+ trace context)          ▲
                              ▼                                        │ claim (SKIP LOCKED), heartbeat, hasil
                        worker 1..N (Go) ── advisory lock per database ┘
                              │ SQL + checkpoint dalam satu transaction
                              ▼
                        database target (PostgreSQL) ── public.dbmp_checkpoints (bukti commit)
```

```
cmd/server, cmd/worker   entry point API dan worker
internal/authz           SELURUH aturan hak akses (level, aksi, approver)
internal/workflow        aturan riwayat linear: apply / rollback / forward / resume, hide rollback
internal/worker          queue consumer: lock, revalidasi, eksekusi normal/batch, retry, recovery
internal/store           query database platform (schema.sql, queue, history)
internal/targetdb        akses database target: viewer, validasi/EXPLAIN, eksekusi + checkpoint
internal/obs             log JSON, tracing OpenTelemetry, metrik Prometheus, pencatat SQL
internal/web             JSON API, middleware (session, CSRF, tracing), penyajian SPA
web/                     SPA React (src/) → dist/ (di-embed)
```

## Access Control (Milestone 1, tetap berlaku)

- Password di-hash **bcrypt**; password koneksi database disimpan terenkripsi **AES-256-GCM** dan tidak pernah dikirim ke SPA.
- Session: identifier acak 256-bit (yang disimpan hanya hash SHA-256), cookie `HttpOnly`, `SameSite=Lax`,
  `Secure` jika `COOKIE_SECURE=true`. Idle timeout 30 menit, absolute expiration 8 jam, rotasi berkala 15 menit,
  grace period 30 detik; rotasi juga saat login dan saat privilege naik (dijadikan admin, masuk group, akses naik,
  dijadikan leader). Pencabutan akses (nonaktif, cabut admin, reset password, hapus user, keluar group,
  akses diturunkan/dicabut, group dihapus) langsung mengakhiri session.
- **CSRF:** setiap request yang mengubah data wajib header `X-CSRF-Token` milik session, body JSON
  (`application/json` memicu preflight CORS), dan ditolak bila `Origin`/`Sec-Fetch-Site` menunjukkan situs lain.
- Semua aturan di [`internal/authz/authz.go`](internal/authz/authz.go), dicek di backend pada setiap request:

| Aksi | Syarat |
|---|---|
| kelola user, group, leader, koneksi, pemberian akses | Platform Admin |
| lihat schema, data, migration, pengajuan, riwayat, status job | View Only atau lebih |
| buat/ubah/hapus draft, validasi, ajukan apply/rollback/forward/resume, jalankan, batalkan pengajuan | View + Edit |
| approve / reject pengajuan | **leader** group yang memiliki akses (view atau edit) ke database itu |

**Akses efektif** = tingkat tertinggi dari semua group user pada database itu (group hanya memberi akses).

## Migration

### Definisi

- **SQL up** (menerapkan perubahan) dan **SQL down** (membatalkan), atau ditandai **irreversible** beserta alasannya.
  `BEGIN/COMMIT/ROLLBACK` buatan user ditolak.
- **Mode normal:** seluruh SQL dalam satu transaction. **Mode batch:** satu statement dengan `$1` (cursor awal,
  eksklusif) dan `$2` (cursor akhir, inklusif) pada kolom unik yang tidak berubah, misalnya
  `UPDATE big_orders SET amount = amount * 2 WHERE id > $1 AND id <= $2`, dengan table, kolom cursor, dan batch size.
- **Immutable:** draft hanya bisa diubah/dihapus selama belum pernah dieksekusi dan tidak sedang diajukan.
  SQL yang pernah dieksekusi tidak bisa ditimpa (perbaikan = migration baru). Pengajuan menyimpan *fingerprint*
  SQL; worker menolak menjalankan definisi yang berubah setelah disetujui.
- **Validasi:** aturan di atas dicek saat disimpan; tombol *Validasi* menjalankan dry-run SQL up lalu down dalam satu
  transaction yang selalu di-rollback (`EXPLAIN`, `EXPLAIN ANALYZE`, atau `EXPLAIN (GENERIC_PLAN)` untuk statement batch).

### Workflow dan approval

```
Draft → Pending Review → Approved → Pending (Queue) → Running → Success / Failed
                       ↘ Rejected / Cancelled                 ↘ Partial / Partially reverted / Uncertain
```

- Setiap group punya **leader** (anggota group, ditetapkan admin).
- Saat diajukan, sistem mencatat satu baris approval untuk **setiap group yang memiliki akses (view atau view+edit)**
  ke database itu. Pengajuan menjadi *Approved* setelah semua leader setuju; satu *Reject* menolak pengajuan.
  Leader dari beberapa group menyetujui untuk semua group yang dipimpinnya sekaligus. Ini berlaku juga untuk
  rollback, forward, dan resume.
- *Jalankan* mempublikasikan job ke queue dan langsung mengembalikan **job ID** (HTTP 202). SPA memantau status
  (polling) tanpa memuat ulang halaman.
- **Revalidasi** di worker, di bawah lock database, tepat sebelum eksekusi: approval lengkap, tidak ada group baru
  yang mendapat akses setelah pengajuan (jika ada, pengajuan gagal dan harus diajukan ulang), definisi tidak berubah,
  dan posisi riwayat masih valid.

### Rollback, forward, dan riwayat

- **Rollback** hanya untuk migration aktif terakhir yang reversible (A → B → C: C dulu, baru B), memakai SQL down asli.
- **Forward** menjalankan ulang SQL up migration yang sudah di-rollback, mengikuti urutan versi (yang tepat di atas
  migration aktif teratas), dan tidak menjalankan migration berikutnya otomatis. Versi diberikan saat commit pertama
  dan tetap sama saat forward.
- Rollback dan forward adalah **eksekusi baru**. Tabel `executions` mencatat **setiap attempt** (berhasil, gagal,
  partial, uncertain, retry): execution ID, migration ID, versi, request/job ID, attempt, pengguna, waktu, status,
  jumlah batch/row, error, trace ID, dan **salinan SQL yang benar-benar dijalankan**. Rollback sukses merujuk
  execution ID yang dibatalkannya. Baris riwayat **tidak dapat diubah atau dihapus** (trigger database).
- **Hide rollback** menyembunyikan pasangan eksekusi yang dibatalkan dan rollback sukses yang membatalkannya
  (dicocokkan dengan execution ID). Kegagalan tetap terlihat, dan filter tidak menghapus data. Contoh spesifikasi
  A[E1], B[E2], rollback B[E3], forward B[E4], C[E5] → E1, E4, E5 (diuji di `workflow_test.go`).

## Asynchronous processing

**Queue:** tabel `jobs` di database platform. Satu job per pengajuan (`UNIQUE`), jadi tidak bisa dipublikasikan dua
kali. Worker mengambil job dengan `UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED)`, sehingga banyak worker
bisa bekerja paralel tanpa mengambil job yang sama. Job dan checkpoint tersimpan di database, jadi tetap ada setelah
restart. Trace context (W3C `traceparent`) ikut disimpan di job.

**Satu migration per database:** worker mengambil **advisory lock PostgreSQL** (`pg_try_advisory_lock`, key = ID
database) pada koneksi khusus dan menahannya selama job berjalan, termasuk di antara commit batch. Lock berlaku
lintas proses worker; database berbeda diproses paralel. Query klaim juga melewati database yang sedang punya job
running atau sedang *ditahan*.

**Mode batch:** batas atas cursor (`max(id)`) ditetapkan saat mulai dan disimpan, sehingga row baru tidak ikut. Setiap
batch: `NextHi` mencari key ke-N setelah cursor (keyset, `ORDER BY … LIMIT`, tanpa memuat data ke memory), lalu
**satu transaction** menjalankan SQL dengan `$1/$2` **dan** menulis checkpoint `(series, batch, lo, hi, rows,
execution)` ke `public.dbmp_checkpoints` di database target, lalu commit. Batch yang gagal hanya me-rollback
transaction-nya; batch sebelumnya tetap committed, dan status menjadi **partial** dengan jumlah batch/row committed.

**Retry dan pembatalan batch:**

- **Resume** (pengajuan baru, perlu approval): melanjutkan dari checkpoint terakhir pada seri yang sama tanpa mengulang batch.
- **Pembatalan batch committed** = rollback pada migration partial: SQL down yang immutable dan disetujui dijalankan
  per batch dengan cakupan `lo..hi` yang tersimpan, **urutan terbalik**. Ini kompensasi per batch, bukan rollback total.
  Jika gagal di tengah, status *partially reverted* dan bisa di-resume. Migration irreversible tidak dapat dibatalkan
  (buat migration perbaikan).
- Selama ada pengajuan **partial / partially reverted / uncertain**, migration lain pada database yang sama **ditahan**:
  pengajuan baru ditolak (409) dan job yang sudah di queue menunggu.

**Kegagalan dan pemulihan:**

| Kejadian | Penanganan |
|---|---|
| Error SQL (constraint, syntax) | transaction di-rollback, attempt tercatat *failed* (batch: *partial* bila ada batch committed). Tidak di-retry otomatis. |
| Gangguan sementara (koneksi putus, deadlock `40P01`, serialization `40001`, lock timeout, worker dimatikan) | job kembali ke queue dengan jeda 2 s, 4 s, 8 s … (maks. 1 menit), maksimal 3 attempt |
| Hasil `COMMIT` tidak pasti (koneksi putus saat commit) | status *uncertain*, database ditahan. Retry **memeriksa bukti dulu**: checkpoint ada = sudah commit, tidak dijalankan ulang. |
| Pesan job duplikat / job diproses dua kali | checkpoint `(series, batch)` adalah primary key di transaction yang sama: commit ganda mustahil, dan job yang menemukan checkpoint langsung menganggapnya selesai |
| Worker mati (crash, kill) | PostgreSQL menutup koneksinya sehingga **advisory lock otomatis lepas**. Job berhenti mengirim heartbeat (tiap 5 s); worker lain menemukannya setelah 30 s (`RecoverStale`), menandai *uncertain*, mengembalikannya ke queue, lalu memeriksa checkpoint sebelum melanjutkan. Worker yang hanya lambat tetap memegang lock, jadi worker lain tidak bisa ikut menjalankan. |
| Worker menerima SIGTERM | eksekusi berhenti di antara batch (atau statement dibatalkan dan di-rollback), hasil dicatat, job di-retry |

## Pengujian data besar

`deploy/sample-db/02_big_orders.sql` membuat `big_orders` berisi **2.000.000 row**. Diukur di Docker Desktop
(Windows 11, PostgreSQL 17):

| Eksekusi | Durasi |
|---|---|
| Satu `UPDATE big_orders SET amount = amount * 2` tanpa batching (psql, 3×, di-rollback) | 8,3 s · 8,7 s · 12,2 s |
| Migration **normal** lewat platform (satu transaction, 2.000.000 row) | 12,0 s |
| Migration **batch** lewat platform, batch size 50.000 → **40 batch**, 2.000.000 row | 9,4 s |
| Rollback batch (kompensasi 40 batch, urutan terbalik) | 9,4 s |

Dataset yang sama dipakai untuk kedua mode. Mode batch tidak harus lebih cepat; keuntungannya: setiap batch
transaction pendek (lock row dilepas tiap batch), progres terlihat (`batch committed / row committed`), dan kegagalan
di tengah hanya mengulang batch yang belum committed.

## Activity logging dan tracing

**Log:** JSON (`log/slog`) ke stdout, tidak disimpan di database platform. Atribut: `time`, `level`, `event`,
`actor`, `database`, `status`, dan ID korelasi `trace_id`/`span_id` (+ `job_id`, `request_id`, `execution_id`,
`attempt`, `batch` bila relevan). Event penting: `auth.login`, `auth.logout`, `admin.*`, `migration.create`,
`request.submit`, `request.approve`, `request.reject`, `job.enqueue`, `job.claimed`, `lock.busy`,
`migration.revalidate`, `migration.execute.start`, `migration.batch.start`, `migration.batch.commit`,
`migration.commit`, `migration.rollback`, `migration.evidence`, `job.finished`, `queue.recover`, `http.request`.
**Setiap eksekusi SQL** ke database target, termasuk query read-only viewer, dicatat sebagai `sql.query` (query,
parameter, durasi, jumlah row, `sqlstate`/error). Query database platform dicatat di level `debug` dengan parameter
disamarkan. Password, token, CSRF, cookie, session ID, dan password di DSN selalu disamarkan (`[REDACTED]`).

**Tracing (OpenTelemetry):** `HTTP span` → `queue.publish` → (trace context disimpan di job) → `queue.process` di
worker → `lock.acquire` → `migration.revalidate` → `migration.batch` / `migration.transaction` → `db.query`. Durasi dan
error tercatat di span. `OTEL_TRACES_EXPORTER=console` mencetak span ke stdout. Contoh output nyata satu trace
(API → queue → worker → batch → query, `trace_id` sama): [`docs/samples/logs.jsonl`](docs/samples/logs.jsonl) dan
[`docs/samples/spans.json`](docs/samples/spans.json).

## Testing

```bash
go test ./...                                                       # unit test
docker compose up -d platform-db
TEST_DATABASE_URL="postgres://dbmp:dbmp@localhost:15432/dbmp?sslmode=disable" go test ./...   # + integration test
```

- **Unit test** (stub, tanpa database): aturan otorisasi, akses efektif dan approver; aturan apply/rollback/forward/resume,
  urutan linear, penahanan database, satu pengajuan aktif, hide rollback (contoh spesifikasi); validasi migration
  (normal, batch, irreversible); klasifikasi error transient dan backoff; redaksi log, `trace_id` di log, propagasi
  trace context lewat queue; session, password, enkripsi, pemecah SQL; penolakan akses di setiap endpoint, CSRF, cookie.
- **Integration test** (handler API + worker + PostgreSQL asli, database baru per test): login/cookie/CSRF/logout/rotasi;
  viewer dan penolakan akses; alur lengkap apply → rollback → forward dengan approval dua leader (non-leader, outsider,
  dan admin tanpa group ditolak); reject/cancel; aturan rollback (bukan terakhir, irreversible) dan forward linear;
  migration gagal di-rollback dan tercatat; dry-run tidak mengubah data; batch migration dan rollback per batch;
  **partial → database ditahan → resume dari checkpoint tanpa mengulang batch**; pembatalan batch committed;
  **bukti commit mencegah commit ganda setelah worker crash** (stale heartbeat → uncertain → retry);
  revalidasi menolak group baru; **beberapa worker paralel tidak pernah menjalankan dua migration bersamaan pada satu
  database** dan versi tetap linear; riwayat tidak bisa diubah/dihapus.

## API

Semua endpoint di bawah `/api`, JSON. `X-CSRF-Token` wajib untuk method selain GET.

| Endpoint | Keterangan |
|---|---|
| `POST /login`, `POST /logout`, `GET /session` | autentikasi |
| `GET /databases`, `GET /reviews` | database yang dapat diakses, pengajuan yang menunggu approval saya |
| `GET /db/{id}/tables`, `…/tables/{schema}/{table}`, `…/rows?offset=` | viewer |
| `GET|POST /db/{id}/migrations`, `GET|PUT|DELETE …/migrations/{mID}`, `POST …/validate` | migration |
| `POST /db/{id}/migrations/{mID}/requests` `{action, target_request_id, note}` | ajukan apply/rollback/forward/resume |
| `GET /db/{id}/requests`, `GET …/requests/{rID}`, `POST …/approve`, `…/reject`, `…/cancel`, `…/execute` | review, approval, eksekusi (202 + `job_id`) |
| `GET /db/{id}/jobs/{jobID}`, `GET /db/{id}/history?hide_rollback=1` | status job, migration history |
| `/admin/users`, `/admin/groups` (`…/leader`, `…/members`, `…/grants`), `/admin/databases` | administrasi |

## Bonus

### Observability

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d --build
```

- App dan worker mengirim **traces dan logs** lewat OTLP/HTTP ke **OpenTelemetry Collector**
  ([`deploy/observability/otel-collector.yaml`](deploy/observability/otel-collector.yaml)), yang meneruskan traces ke
  **Tempo** dan logs ke **Loki**. Log tetap juga keluar di stdout.
- **Metrik Prometheus** di `METRICS_ADDR` (`:9090/metrics` di app dan setiap worker, port terpisah dari aplikasi).
  Prometheus ([`prometheus.yml`](deploy/observability/prometheus.yml)) men-scrape app dan setiap replika worker (DNS).

| Metrik | Isi |
|---|---|
| `dbmp_jobs{status}` | job queued / running / done / failed |
| `dbmp_jobs_completed_total{result}`, `dbmp_job_retries_total` | job selesai per hasil, retry |
| `dbmp_queue_wait_seconds`, `dbmp_lock_wait_seconds` | waktu tunggu queue dan lock (histogram) |
| `dbmp_migration_duration_seconds{action,mode,result}` | durasi migration |
| `dbmp_migration_batches_total{direction}`, `dbmp_migration_rows_total{direction}` | batch/row diproses |
| `dbmp_db_pool_connections{pool,state}`, `dbmp_db_pool_acquire_wait_seconds_total{pool}`, `dbmp_db_pool_acquires_total{pool}` | koneksi aktif/idle, waktu tunggu koneksi (pool `platform`/`target`) |
| `dbmp_http_requests_total{route,method,code}`, `dbmp_http_request_duration_seconds{route,method}` | API |

  Label hanya bernilai terbatas (pola route, status, aksi); tidak ada user ID, job ID, atau SQL sebagai label.
- **Grafana** <http://localhost:3000> (provisioning otomatis: datasource + dashboard
  [`dbmp.json`](deploy/observability/grafana/dashboards/dbmp.json)) menampilkan job per status, throughput, retry,
  waktu tunggu queue dan lock, durasi migration, batch/row per detik, koneksi aktif/idle, waktu tunggu koneksi,
  serta request rate, p95, dan error rate API.
- **Korelasi:** setiap log membawa `trace_id`. Di Grafana → Explore → Loki, field `trace_id` menjadi tautan ke trace di
  Tempo, dan dari span di Tempo tersedia *Logs for this span* (query Loki dengan `trace_id` yang sama). Panel metrik dan
  trace dicocokkan berdasarkan waktu/route. Riwayat migration di SPA juga menampilkan `trace_id` tiap eksekusi.
  Contoh hasil nyata (log Loki → trace Tempo yang mencakup server dan worker, plus metrik):
  [`docs/samples/korelasi.md`](docs/samples/korelasi.md).

### Stress testing dengan k6

Script: [`loadtest/stress.js`](loadtest/stress.js). Setup membuat group `k6` (admin sebagai leader) dan dua koneksi
(`k6-lab`, `k6-shop`) yang hanya bisa diakses group itu. Dua skenario berjalan bersamaan:

- `api`: user membuka daftar database, table, row, migration, dan riwayat. Beban normal 10 VU → 30 VU → 60 VU (stress)
  → kembali 10 VU (pemulihan), tiap tahap 30 s.
- `jobs`: user membuat migration, mengajukan, menyetujui, menjalankan (job ke queue), lalu **memantau job sampai worker
  selesai**. Respons 202 tidak dihitung sukses; hanya status `success` dari worker. VU bergantian ke dua database:
  job pada database yang sama antre di lock, job pada database berbeda berjalan paralel. 2 → 6 → 2 VU.
- **Threshold:** API p95 < 500 ms dan p50 < 150 ms, `http_req_failed` < 1%, `job_success` > 95%, job end-to-end p95 < 30 s.

```bash
docker run --rm -v "$PWD/loadtest:/scripts" -e BASE_URL=http://host.docker.internal:8080 \
  -e TARGET_HOST=sample-db grafana/k6:0.54.0 run --out json=/scripts/results/raw.json /scripts/stress.js
python loadtest/stages.py loadtest/results/raw.json      # statistik per tahap beban
```

Hasil (server + 2 worker di laptop Windows 11, PostgreSQL di Docker Desktop, stack observability ikut berjalan; total
4 menit 11 s, semua threshold lulus, ringkasan di [`loadtest/results/summary.json`](loadtest/results/summary.json)
dan [`loadtest/results/k6-output.txt`](loadtest/results/k6-output.txt)):

| Tahap | API req/s | API p50 (ms) | API p95 (ms) | Error rate | Job selesai/s | Queue wait p50 / p95 (ms) | Job e2e p50 / p95 (ms) |
|---|---|---|---|---|---|---|---|
| normal: api 10 VU, jobs 2 VU | 47,2 | 8,1 | 37,6 | 0,00% | 1,90 | 644 / 915 | 1058 / 1110 |
| 3×: api 30 VU, jobs 2→6 VU | 135,8 | 12,9 | 46,3 | 0,00% | 2,80 | 798 / 943 | 1036 / 3071 |
| 6× (stress): api 60 VU, jobs 6 VU | 280,0 | 11,1 | 32,0 | 0,00% | 5,23 | 647 / 975 | 1045 / 1093 |
| pemulihan: api 10 VU, jobs 2 VU | 46,8 | 8,0 | 29,7 | 0,00% | 1,70 | 612 / 960 | 1036 / 1085 |

Total: 34.453 request (137,2/s), 0 error; 696 job selesai (2,77/s), 100% sukses, 0 retry
(`dbmp_job_retries_total`); eksekusi job p50 26 ms, p95 100 ms.

**Analisis.**
- Throughput API naik hampir linear dengan jumlah VU (47 → 136 → 280 req/s) tanpa error, dan p95 tetap di bawah
  50 ms di semua tahap (jauh dari batas 500 ms). Pada 6× beban p95 justru sedikit lebih rendah daripada 3× karena
  koneksi pool dan cache PostgreSQL sudah hangat; perbedaan sekecil ini masih dalam variasi antar-run. Batas atas
  belum tercapai di 60 VU, jadi titik jenuh ada di atas beban ini. Setelah beban turun, p50 kembali ke ±8 ms dan p95
  ke ±30 ms; sistem pulih tanpa restart.
- Throughput job naik dari 1,9 ke 5,2 job/s karena job pada dua database berbeda berjalan paralel di dua worker,
  sedangkan job pada database yang sama tetap berurutan (lock).
- Pada tahap 3× (saat job naik 2 → 6 VU) ada lonjakan job end-to-end p95 3,1 s dengan eksekusi terlama 2,2 s. Waktu
  tunggu queue tidak ikut naik, jadi lonjakan ada di eksekusi SQL di database target, bukan di queue atau lock
  worker. Lonjakan ini sesaat (tidak berulang pada 6×, median tetap ±1 s) dan dapat ditelusuri di Tempo lewat span
  `queue.process`/`migration.transaction` dengan durasi terlama.
- Waktu tunggu queue (±0,6–1 s) didominasi interval polling worker (1 s saat queue kosong), bukan beban; eksekusi SQL
  sendiri hanya puluhan ms. Untuk mempercepat: interval polling lebih pendek atau `LISTEN/NOTIFY`, dan tambah worker
  (`--scale worker=N`) untuk lebih banyak database paralel.

### Security: SAST dengan CodeQL

Lihat [`docs/codeql.md`](docs/codeql.md): workflow [`.github/workflows/codeql.yml`](.github/workflows/codeql.yml)
(Go dan JavaScript, suite `security-extended`), hasil scan, dan keputusan untuk setiap temuan.
