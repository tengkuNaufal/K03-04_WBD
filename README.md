# DBMP — Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026 — Milestone 1.

Platform untuk mengatur akses ke database PostgreSQL bersama, menjelajahi schema dan data,
serta menjalankan perubahan schema lewat **migration** yang tercatat.

- **Backend:** Go (`net/http`, `pgx/v5`)
- **Frontend:** Go template (server-rendered, bukan SPA)
- **Database target & platform:** PostgreSQL

## Menjalankan

```bash
docker compose up -d --build
```

Buka <http://localhost:8080>, login `admin` / `admin12345`.

Mencoba database contoh `shop`:

1. **Koneksi → + Koneksi baru**: host `sample-db`, port `5432`, database `shop`, user `sample`, password `sample`.
2. **Groups → buat group**, tambahkan anggota, beri akses database (View Only / View + Edit).
3. Buka **Database** → `shop`.

> Platform Admin bukan tingkat akses database. Admin juga harus masuk group untuk melihat isi database.

Tanpa container app (untuk development):

```bash
docker compose up -d platform-db sample-db
export DATABASE_URL="postgres://dbmp:dbmp@localhost:15432/dbmp?sslmode=disable"
export APP_SECRET_KEY="ganti-dengan-rahasia-panjang"
export ADMIN_USERNAME=admin ADMIN_PASSWORD=admin12345
go run ./cmd/server
```

Environment variable: `DATABASE_URL` (wajib), `APP_SECRET_KEY` (wajib, ≥16 karakter),
`APP_ADDR` (default `:8080`), `COOKIE_SECURE` (`true` di HTTPS), `ADMIN_USERNAME`/`ADMIN_PASSWORD`
(admin pertama, dibuat jika belum ada user).

## Struktur kode

```
cmd/server/          entry point dan bootstrap admin
internal/authz/      SELURUH aturan hak akses dan aturan akses efektif
internal/auth/       hashing password, login, logout
internal/session/    session (idle timeout, absolute expiration, rotasi, grace period)
internal/secret/     enkripsi password koneksi (AES-256-GCM)
internal/store/      query ke database platform + schema.sql
internal/targetdb/   akses database target: viewer, eksekusi migration, EXPLAIN, pemecah SQL
internal/web/        routing, middleware (session, CSRF), handler
internal/integration integration test
web/                 template HTML, CSS, JS
```

## Access Control

**Authentication**
- Password di-hash dengan **bcrypt** (cost 12).
- Password dan session id tidak pernah ada di URL, response, atau log.
- Password koneksi database target disimpan terenkripsi AES-256-GCM dan tidak pernah ditampilkan.

**Authorization** — semua aturan ada di [`internal/authz/authz.go`](internal/authz/authz.go) (tabel `rules`):

| Aksi | Syarat |
|---|---|
| kelola user, group, koneksi, pemberian akses | Platform Admin |
| lihat schema, data, riwayat migration | View Only atau lebih |
| buat, ubah, hapus, EXPLAIN, jalankan migration | View + Edit |

Pengecekan dilakukan di backend pada setiap request (middleware `admin` dan `loadDB`), bukan hanya
dengan menyembunyikan tombol. Aksi yang tidak terdaftar selalu ditolak.

**Akses efektif:** user bisa masuk banyak group, dan akses hanya diberikan ke group.
**Akses efektif = tingkat tertinggi dari semua group user** pada database itu. Contoh: group A
View Only + group B View + Edit → View + Edit. Group hanya memberi akses, tidak pernah melarang,
jadi mengambil yang tertinggi aman. Akses dihitung ulang setiap request, sehingga pencabutan
langsung berlaku.

**Session**

| Parameter | Nilai | Alasan |
|---|---|---|
| Identifier | 256-bit acak, yang disimpan hanya hash SHA-256 | Opaque; bocornya tabel tidak bisa dipakai login |
| Idle timeout | 30 menit | Komputer yang ditinggal tidak terbuka lama |
| Absolute expiration | 8 jam | Satu hari kerja, lalu wajib login ulang |
| Rotasi berkala | 15 menit | Identifier yang bocor cepat tidak berguna |
| Grace period | 30 detik | Request paralel saat rotasi tidak gagal |

- Cookie `dbmp_session`: `HttpOnly`, `SameSite=Lax`, `Secure` jika `COOKIE_SECURE=true`.
- Identifier dirotasi **saat login**, **saat privilege naik** (dijadikan admin, masuk group, atau
  akses group naik), dan **berkala**. Nama cookie tetap, nilainya diganti. Identifier lama masih
  diterima selama grace period lalu ditolak.
- Logout mencabut session di backend. **Pencabutan akses juga mencabut session** user yang terdampak:
  menonaktifkan user, mencabut admin, reset password, menghapus user, mengeluarkan user dari group,
  menurunkan/mencabut akses group, atau menghapus group. User tersebut harus login ulang.
- **CSRF:** setiap POST wajib membawa token CSRF milik session dan ditolak jika `Origin` dari situs
  lain. Form login memakai double-submit cookie.

## Database Management
- Admin mengelola koneksi; koneksi **diuji sebelum disimpan**.
- Viewer menampilkan schema, table/view, column, constraint, index, dan data (paginasi 50 baris).
- Query viewer berjalan di transaksi **READ ONLY** dengan `statement_timeout`; nama table di-quote.
- Database yang tidak dapat dihubungi tampil sebagai error 502 yang jelas tanpa membocorkan kredensial;
  penolakan dari database target (role tidak punya hak) tampil sebagai 403.

## Migration
- Editor SQL; migration disimpan sebagai draft, bisa diubah sebelum dijalankan.
- Hanya **View + Edit** yang dapat membuat, mengubah, dan menjalankan migration.
- Seluruh SQL dijalankan dalam **satu transaction**: `COMMIT` jika semua berhasil, `ROLLBACK` jika
  ada yang gagal. `BEGIN/COMMIT/ROLLBACK` buatan user ditolak.
- **Riwayat linear:** versi 1, 2, 3, … diberikan saat commit berhasil. Eksekusi per database
  dikunci dengan `SELECT ... FOR UPDATE`, jadi versi tidak bentrok walau dijalankan bersamaan.
- Tercatat: versi, SQL, pembuat, pelaksana, waktu, status (`draft`/`applied`/`failed`), dan error.
- Migration yang sudah applied tidak bisa diubah, dijalankan ulang, atau dihapus.

**Bonus — Query execution plan analysis:** tombol *Analisis execution plan* menjalankan `EXPLAIN`
atau `EXPLAIN ANALYZE` untuk tiap statement sebelum eksekusi (jenis scan, index, join, cost, waktu,
jumlah row). Semuanya di dalam transaction yang selalu di-rollback.

## Testing

```bash
go test ./...                       # unit test

docker compose up -d platform-db    # + integration test
TEST_DATABASE_URL="postgres://dbmp:dbmp@localhost:15432/dbmp?sslmode=disable" go test ./...
```

- **Unit test** (dependency diganti stub, tanpa database): aturan otorisasi dan akses efektif,
  siklus session (idle, absolute, rotasi, grace period, revoke), hashing password dan login,
  enkripsi, pemecah SQL dan validasi migration, CSRF, serta penolakan akses di setiap handler
  (non-admin ke halaman admin, View Only membuat/mengubah/menjalankan migration, user tanpa group).
- **Integration test:** HTTP handler asli + PostgreSQL asli (database sementara per test): cookie,
  login, CSRF, logout, rotasi saat privilege naik, halaman admin, viewer, penolakan akses, akses
  efektif, pencabutan session saat akses dicabut, alur migration (commit, rollback, versi linear, eksekusi bersamaan), EXPLAIN, dan
  database yang tidak dapat dihubungi.

Tanpa `TEST_DATABASE_URL`, integration test di-skip.

## Kelompok 04 K03

* 13524012 - Tengku Naufal Saqib
* 13524120 - Jonathan Alveraldo Bangun
* 13524147 - Muh. Hartawan Haidir