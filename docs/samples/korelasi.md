# Contoh korelasi log, trace, dan metrik

Diambil dari stack `docker-compose.observability.yml` setelah 6 batch migration (apply + rollback) dijalankan
pada database `lab` lewat API.

## Log (Loki) → trace (Tempo)

Query Loki `{service_name="dbmp-worker"} |= "batch committed"` mengembalikan log dengan structured metadata:

```
event=migration.batch.commit  service_name=dbmp-worker  job_id=12
trace_id=bae63ffc3f87e56f0a1a66643e190691  span_id=08d8a6b129aec4bd
```

Trace `bae63ffc3f87e56f0a1a66643e190691` di Tempo berisi span dari **dua proses**, dihubungkan oleh trace context
yang disimpan di job queue:

```
dbmp-server  POST /api/db/{dbID}/requests/{rID}/execute
dbmp-server  └─ queue.publish
dbmp-server     └─ db.query (INSERT INTO jobs …)
dbmp-worker  queue.process            (parent = queue.publish, lewat traceparent di jobs.trace_context)
dbmp-worker  ├─ lock.acquire
dbmp-worker  ├─ migration.revalidate ── db.query …
dbmp-worker  ├─ migration.batch (batch 1..N) ── db.query (UPDATE …, INSERT dbmp_checkpoints, commit)
dbmp-worker  └─ db.query …
```

Di Grafana: Explore → Loki → klik field `trace_id` (derived field) untuk membuka trace di Tempo. Dari span di Tempo,
*Logs for this span* menjalankan query Loki dengan `trace_id` yang sama. Riwayat migration di SPA juga menampilkan
`trace_id` setiap eksekusi.

## Metrik (Prometheus)

```
sum by (status) (dbmp_jobs)       → done=12, queued=0, running=0, failed=0
sum(dbmp_migration_batches_total) → 216
targets: dbmp-server app:9090 (up), dbmp-worker 172.21.0.10:9090 (up), dbmp-worker 172.21.0.11:9090 (up)
```

Lonjakan `dbmp_migration_duration_seconds` atau `dbmp_queue_wait_seconds` di dashboard dicocokkan dengan trace pada
rentang waktu yang sama (Tempo search `service.name=dbmp-worker name=queue.process`, urutkan berdasarkan durasi),
lalu log trace itu dibuka lewat `trace_id`.

Contoh output mentah exporter console (log JSON dan span): [`logs.jsonl`](logs.jsonl), [`spans.json`](spans.json).
