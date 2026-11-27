# SAST dengan CodeQL

## Konfigurasi

Workflow [`.github/workflows/codeql.yml`](../.github/workflows/codeql.yml) berjalan pada setiap push ke `main`,
`develop`, dan `milestone-2`, setiap pull request ke `main`/`develop`, setiap Senin (terjadwal), dan manual
(`workflow_dispatch`).

| Bahasa | Build mode | Cakupan |
|---|---|---|
| Go | `autobuild` | `cmd/`, `internal/` (API, worker, store, akses database target, observability), `web/embed.go` |
| JavaScript/TypeScript | `none` | `web/src/` (SPA React), `loadtest/stress.js` |

Query suite: **`security-extended`** (lebih luas dari default, termasuk temuan severity rendah/medium).
Hasil tampil di repository: **Security → Code scanning**.

Cara mengaktifkan: push branch ke GitHub. Untuk repository private perlu GitHub Advanced Security; untuk repository
public tersedia gratis.

## Hasil scan

> Diisi setelah workflow pertama selesai: tanggal, commit, jumlah alert per bahasa, dan tautan ke halaman Code scanning.
> Hasil bersih tidak berarti aplikasi pasti aman.

| # | Rule | Severity | Lokasi | Dampak | Keputusan | Alasan / commit perbaikan |
|---|---|---|---|---|---|---|
| | | | | | | |

## Area yang perlu diperhatikan saat menilai temuan

Platform ini **sengaja menjalankan SQL dari user** (migration, validasi/EXPLAIN). Karena itu CodeQL kemungkinan
menandai aliran data dari request ke eksekusi query (`go/sql-injection`). Penilaiannya:

| Lokasi | Mengapa SQL dari user | Kontrol yang ada |
|---|---|---|
| `internal/targetdb/run.go` (`RunOnce`, `RunBatch`), `internal/targetdb/exec.go` (`Explain`) | fitur inti: menjalankan SQL migration / dry-run | hanya level View + Edit, wajib approval semua leader, revalidasi di worker, dijalankan dengan role koneksi database target (bukan database platform), `statement_timeout`, dry-run selalu di-rollback |
| `internal/targetdb/inspect.go` (`Rows`), `run.go` (`ScopeMax`, `NextHi`) | nama table/kolom tidak bisa diparameterkan | identifier di-quote dengan `pgx.Identifier.Sanitize()`, nama batch divalidasi regex, limit/offset integer |

Kemungkinan temuan lain dan kontrolnya:

- **Logging data sensitif** (`go/clear-text-logging`): log memakai redaksi (`internal/obs/log.go`, diuji di
  `obs_test.go`); parameter query database platform tidak pernah dicatat.
- **Path traversal saat menyajikan SPA** (`go/path-injection`): `spa.go` membaca dari `embed.FS` dengan
  `path.Clean`, sehingga tidak bisa keluar dari folder build.
- **CSRF / cookie**: cookie `HttpOnly` + `SameSite=Lax` (+ `Secure` di HTTPS), token CSRF di header, cek `Origin` dan
  `Sec-Fetch-Site`.
