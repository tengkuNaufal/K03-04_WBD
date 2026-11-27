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

| | |
|---|---|
| Tanggal | 27 November 2026 |
| Commit | `9019b76` (`main`, Merge PR #11). Alert pertama kali terdeteksi di `a0fdad2` (`develop`) |
| Workflow | CodeQL, job `Analyze (go)` dan `Analyze (javascript-typescript)`: keduanya sukses |
| Hasil | **5 alert terbuka**: Go 5 (1 High, 4 Medium), JavaScript/TypeScript 0 |
| Halaman | [Security → Code scanning](https://github.com/tengkuNaufal/K03-04_WBD/security/code-scanning) |

| # | Rule | Severity | Lokasi | Dampak | Keputusan | Alasan |
|---|---|---|---|---|---|---|
| 1 | `go/weak-sensitive-data-hashing` (CWE-327/328/916) | High | `internal/secret/secret.go:21` | Jika nilai yang di-hash adalah password user, SHA-256 terlalu cepat sehingga mudah di-brute-force | **Risiko diterima** (false positive untuk konteks ini) | Lihat penjelasan #1 di bawah |
| 2 | `go/log-injection` (CWE-117) | Medium | `internal/web/handlers_admin.go:20` (`audit`) | User memalsukan baris log baru lewat newline di input | **False positive** | Log ditulis `slog.NewJSONHandler`; lihat penjelasan #2–#5 |
| 3 | `go/log-injection` (CWE-117) | Medium | `internal/web/handlers_auth.go:39` (username login gagal) | sama | **False positive** | sama |
| 4 | `go/log-injection` (CWE-117) | Medium | `internal/web/handlers_migration.go:246` (`action` pengajuan) | sama | **False positive** | sama |
| 5 | `go/log-injection` (CWE-117) | Medium | `internal/web/server.go:343` (`r.URL.Path` saat error 500) | sama | **False positive** | sama |

**#1: SHA-256 untuk kunci enkripsi.** Yang di-hash adalah `APP_SECRET_KEY`, yaitu rahasia konfigurasi server (minimal
16 byte), **bukan password user**. CodeQL menganggapnya password karena nama variabelnya (`secretKey`). SHA-256 di sini
hanya mengubah rahasia itu menjadi kunci AES-256-GCM sepanjang 32 byte untuk mengenkripsi password koneksi database.
Hash tersebut tidak pernah disimpan atau dikirim, sehingga tidak ada yang bisa di-brute-force secara offline dari hash.
Password user tetap di-hash dengan **bcrypt** (`internal/auth/password.go`). Risiko yang tersisa: jika `APP_SECRET_KEY`
lemah, kuncinya dapat ditebak dari ciphertext yang bocor. Karena itu rahasia ini wajib panjang dan acak saat deploy.
Nilai default di `docker-compose.yml` hanya untuk development. Perbaikan lanjutan yang disarankan adalah HKDF
(`crypto/hkdf`). Perubahan ini tidak dilakukan sekarang karena akan membuat password koneksi yang sudah terenkripsi
tidak dapat didekripsi tanpa migrasi data.

**#2–#5: log injection.** Semua log ditulis dengan `slog.NewJSONHandler` (`internal/obs/log.go:15`), dan nilai dari
user selalu menjadi **nilai atribut**, bukan bagian dari format pesan. Encoder JSON meng-escape `\n`, `\r`, `"`, dan
karakter kontrol, sehingga input tidak bisa membuat baris log baru atau atribut palsu. Pengiriman ke OpenTelemetry
Collector juga berupa atribut terstruktur. Bukti, login dengan username
`x\n{"level":"ERROR","msg":"FORGED"}` menghasilkan **satu** baris:

```
{"level":"INFO","msg":"login failed","event":"auth.login","status":"failed","actor":"x\n{\"level\":\"error\",\"msg\":\"forged\"}",...}
```

Tidak ada baris log yang diawali `{"level":"ERROR","msg":"FORGED"`. Nilai rahasia tetap disamarkan oleh `redactAttr`
(diuji di `TestLoggerRedactsSecrets`).

Kesimpulan: tidak ada temuan yang memerlukan perubahan kode saat ini. Hasil ini tidak berarti aplikasi pasti aman,
karena CodeQL hanya mendeteksi pola yang dikenali query-nya.

## Cakupan dan hal yang tidak ditandai

Platform ini **sengaja menjalankan SQL dari user** (migration, validasi/EXPLAIN). CodeQL **tidak** menandai
`go/sql-injection` pada scan ini, tetapi area tersebut tetap dikontrol sebagai berikut:

| Lokasi | Mengapa SQL dari user | Kontrol yang ada |
|---|---|---|
| `internal/targetdb/run.go` (`RunOnce`, `RunBatch`), `internal/targetdb/exec.go` (`Explain`) | fitur inti: menjalankan SQL migration / dry-run | hanya level View + Edit, wajib approval semua leader, revalidasi di worker, dijalankan dengan role koneksi database target (bukan database platform), `statement_timeout`, dry-run selalu di-rollback |
| `internal/targetdb/inspect.go` (`Rows`), `run.go` (`ScopeMax`, `NextHi`) | nama table/kolom tidak bisa diparameterkan | identifier di-quote dengan `pgx.Identifier.Sanitize()`, nama batch divalidasi regex, limit/offset integer |

Kontrol lain yang relevan (tidak ada alert untuk rule-rule ini):

- **Logging data sensitif** (`go/clear-text-logging`): log memakai redaksi (`internal/obs/log.go`, diuji di
  `obs_test.go`); parameter query database platform tidak pernah dicatat.
- **Path traversal saat menyajikan SPA** (`go/path-injection`): `spa.go` membaca dari `embed.FS` dengan
  `path.Clean`, sehingga tidak bisa keluar dari folder build.
- **CSRF / cookie**: cookie `HttpOnly` + `SameSite=Lax` (+ `Secure` di HTTPS), token CSRF di header, cek `Origin` dan
  `Sec-Fetch-Site`.
