# Test Architecture, Consolidation & Acceptance Matrix Audit

Dokumen ini mendokumentasikan arsitektur pengujian MyCaffe Core POS, strategi konsolidasi test suite, kebijakan pemisahan lapisan tes (*test layering*), serta inventarisasi skenario operasional End-to-End (E2E).

---

## 1. Inventarisasi Test Suite Aktual

Pemeriksaan pada commit `316cfa9`:

| Test File | Lapisan | Baris | Target Pengujian | Kebutuhan Lingkungan | Status CI |
| ----------- | --------- | ------- | ------------------ | ---------------------- | ----------- |
| `internal/service/pricing_test.go` | Unit Test Murni | 436 | Kalkulasi pembulatan BPS, modifier sub-addon pricing, line pricing | Tanpa DB / Network | Selalu jalan di job `build-and-test` |
| `internal/domain/payment_test.go` | Unit Test Murni | 336 | Validasi state machine pembayaran dan enum | Tanpa DB / Network | Selalu jalan di job `build-and-test` |
| `internal/config/config_test.go` | Unit Test Murni | 271 | Parsing env, default values, validation | Tanpa DB / Network | Selalu jalan di job `build-and-test` |
| `internal/handler/iteration_4a_integration_test.go` | HTTP Integration | 495 | Modifier CRUD, quote POS/Publik, 409 conflict, fingerprint | PostgreSQL 18 live | Lulus di job `database-migration-test` |
| `internal/service/payment_service_integration_test.go` | Service Integration | 785 | Transaksi pembayaran tunai/gateway, webhook Midtrans, idempotency | PostgreSQL 18 live | Lulus di job `database-migration-test` |
| `internal/service/order_service_integration_test.go` | Service Integration | 683 | Order creation, stock reservation, concurrent cancellation | PostgreSQL 18 live | Lulus di job `database-migration-test` |
| `internal/service/auth_service_integration_test.go` | Service Integration | 370 | Login, refresh tokens, RLS switching | PostgreSQL 18 live | Lulus di job `database-migration-test` |
| `internal/service/staff_lifecycle_integration_test.go` | Service Integration | 283 | Undangan staf, aktivasi PIN, lockout, password reset | PostgreSQL 18 live | Lulus di job `database-migration-test` |
| `internal/handler/catalog_integration_test.go` | HTTP Integration | 270 | CRUD Kategori dan Produk via HTTP | PostgreSQL 18 live | Lulus di job `database-migration-test` |
| `internal/pkg/database/postgres_test.go` | Database Integration | 95 | Direct login `mycaffe_app`, verifikasi RLS isolation | PostgreSQL 18 live | Lulus di job `database-migration-test` |

---

## 2. Prinsip Konsolidasi & Piramida Pengujian

### 2.1 Lapisan 1 — Unit Tests Murni (In-Memory, Super Cepat)

- **Karakteristik**: Berjalan tanpa database PostgreSQL, tanpa Docker, dan tanpa network I/O.
- **Cakupan**:
  - Validasi formula `expected_cash = opening_float + cash_sales - cash_refunds - paid_outs + cash_ins`.
  - Kalkulasi `variance = counted_cash - expected_cash`.
  - Pembulatan matematis mata uang IDR integer `int64`.
  - Validasi transisi status shift (`open` -> `closed` / `force_closed`).
  - Matriks evaluasi hak akses RBAC (`rbac.Has(...)`).
  - Kalkulasi hash deterministik SHA-256 untuk quote fingerprint dan request hash.

### 2.2 Lapisan 2 — Repository Integration Tests (PostgreSQL 18 Asli)

- **Karakteristik**: Terhubung ke database uji PostgreSQL 18 menggunakan role non-superuser `mycaffe_app`.
- **Cakupan**:
  - Penegakan kebijakan Row-Level Security (RLS) antar-tenant.
  - Parsial Unique Index (misal `idx_shifts_open_drawer` memastikan tepat 1 shift terbuka per drawer).
  - Explicit row locking (`SELECT ... FOR UPDATE`).
  - Trigger integritas ledger finansial (trigger anti-tampering `cash_movements` yang menolak UPDATE dan DELETE).

### 2.3 Lapisan 3 — Service Integration Tests (ACID Boundaries)

- **Karakteristik**: Menguji batas transaksi atomik (`database.WithTenantTx`).
- **Cakupan**:
  - Atomisitas pembayaran tunai: sukses pembayaran wajib mencatat entri `cash_movements` (tipe `sale`) dalam transaksi yang sama.
  - Idempotency replay: pengulangan request pembayaran dengan `idempotency_key` yang sama tidak boleh mencatat mutasi kas ganda.

### 2.4 Lapisan 4 — HTTP Integration Tests

- **Karakteristik**: Menggunakan `httptest.ResponseRecorder` dan router Gin.
- **Cakupan**:
  - Validasi middleware autentikasi dan penegakan izin RBAC.
  - Penolakan akses role negatif (misal: barista/kitchen ditolak saat mencoba membuka shift atau mengakses POS Quote).
  - Verifikasi kode status HTTP dan format payload JSON error sesuai kontrak.

---

## 3. Rencana Reorganisasi Penamaan Test (Capability-Based)

Untuk menghilangkan penamaan berbasis riwayat iterasi/agent, struktur pengujian akan dialihkan secara bertahap pada PR 4b-0 dan PR 4b-1:

```text
internal/
  service/
    pricing_test.go                     # Pure math & BPS rounding
    shift_balance_test.go               # Pure shift expected cash & variance logic
    order_integration_test.go           # Order creation & stock ACID
    payment_integration_test.go         # Payment settlement & webhook
    shift_integration_test.go           # Shift lifecycle & cash movements ACID

  handler/
    catalog_modifier_integration_test.go # (Sebelumnya iteration_4a_integration_test.go)
    shift_http_integration_test.go       # HTTP POS shift, blind count, X/Z reports
```

*Aturan*: Pemindahan file test dilakukan secara mekanis tanpa menghapus assertion yang telah ada, dan diverifikasi dengan perbandingan jumlah subtest sebelum dan sesudah refactoring.

---

## 4. Matriks 30 Skenario Operasional E2E POS Acceptance

| # | Kasus Uji Operasional | Lapisan | Ekspektasi Perilaku Bisnis |
| --- | ----------------------- | --------- | ---------------------------- |
| 1 | Kasir login & buka shift baru dengan modal awal Rp 100.000 | E2E | Shift berstatus `open`, tercatat `cash_movements` tipe `opening_float` Rp 100.000. |
| 2 | Dua kasir mencoba membuka drawer yang sama bersamaan | Concurrency | Transaksi kedua ditolak `409 CONFLICT` oleh parsial unique index. |
| 3 | Kasir memproses transaksi tunai tanpa membuka shift | HTTP / Service | Ditolak seketika dengan `409 NO_OPEN_SHIFT`. Order tetap belum lunas. |
| 4 | Kasir memproses transaksi tunai pada laci berbeda yang tidak aktif | HTTP / Service | Ditolak `409 NO_OPEN_SHIFT`. |
| 5 | Transaksi tunai berhasil diproses di POS | Transaksional | Order lunas, payment tercatat, dan `cash_movements` tipe `sale` dibuat dalam 1 transaksi DB. |
| 6 | Transaksi tagihan Rp 85.000 dibayar uang Rp 100.000 (kembalian Rp 15.000) | Matematika Uang | Mutasi kas yang dicatat tepat Rp 85.000 (nilai riil kas bertambah). Response membawa `change: 15000` dan `open_drawer: true`. |
| 7 | Replay request pembayaran tunai dengan idempotency key yang sama | Idempotensi | Mengembalikan status pembayaran sebelumnya; TIDAK menduplikasi catatan pembayaran atau mutasi kas. |
| 8 | Simulasi kegagalan penyimpanan mutasi kas pada pembayaran | Transaksional | Seluruh transaksi di-rollback; order tetap unpaid, payment tidak tersimpan. |
| 9 | Simulasi kegagalan update order saat pembayaran | Transaksional | Seluruh transaksi di-rollback; kas tidak bertambah. |
| 10 | Pembayaran non-tunai (QRIS / EDC) selesai di POS | Finansial | Order lunas, tetapi `expected_cash` di laci kas fisik TIDAK bertambah. |
| 11 | Pengeluaran kas operasional kasir (*paid-out* Rp 25.000 untuk beli es batu) | Operasional | Memerlukan alasan tertulis, mengurangi `expected_cash`, menghasilkan `open_drawer: true`. |
| 12 | Penambahan modal kas di tengah shift (*cash-in* Rp 50.000) | Operasional | Memerlukan alasan tertulis, menambah `expected_cash`, menghasilkan `open_drawer: true`. |
| 13 | Kasir membuka laci kas tanpa mutasi finansial (*no-sale* untuk tukar receh) | Audit Fraud | Wajib menyertakan alasan tertulis, nilai finansial Rp 0, memicu sinyal `open_drawer: true`, dan tercatat di X/Z report. |
| 14 | Kasir melakukan *blind cash count* saat penutupan shift | Rekonsiliasi | Kasir mengirimkan jumlah uang fisik tanpa melihat nilai `expected_cash` sistem. |
| 15 | Selisih kas (*variance*) berada di dalam toleransi toko ($\le$ Rp 5.000) | Rekonsiliasi | Shift langsung tertutup (`closed`), laporan Z final diterbitkan. |
| 16 | Selisih kas melebihi batas toleransi toko ($>$ Rp 5.000) tanpa PIN owner | Otorisasi | Ditolak dengan `422 VARIANCE_EXCEEDS_TOLERANCE`. Shift tetap terbuka. |
| 17 | Kasir memasukkan PIN owner yang salah saat otorisasi selisih kas | Keamanan | Ditolak `401 INVALID_OWNER_PIN`. Kegagalan dicatat di audit log. |
| 18 | Kasir memasukkan PIN owner yang benar saat otorisasi selisih kas | Otorisasi & Audit | Shift tertutup (`closed`), ID owner pencatat override disimpan, dan diaudit di `audit_logs`. |
| 19 | Race condition: Transaksi tunai masuk bersamaan dengan penutupan shift | Konkurensi | Serialisasi locking: transaksi tunai harus selesai sebelum shift menutup, atau ditolak jika shift telah tertutup. |
| 20 | Shift kasir tetap terbuka melewati tengah malam (pukul 00:00) | Kebijakan Operasional | Shift tetap aktif dan tidak tertutup otomatis; transaksi tetap teratribusi pada shift fisik yang sama. |
| 21 | Owner melakukan *force-close* pada shift yang ditinggalkan kasir | Otorisasi Owner | Shift berstatus `force_closed`, wajib ada alasan tertulis, tercatat di audit log. |
| 22 | Kasir membaca X Report saat shift masih terbuka | Laporan X | Mengembalikan rincian performa berjalan; nilai `expected_cash` di-redact agar tidak membatalkan sifat blind count. |
| 23 | Laporan Z Report dicetak setelah penutupan shift | Rekonsiliasi | Snapshot final membekukan saldo awal, penjualan per metode, mutasi kas, diskon, dan selisih kas final. |
| 24 | Akses shift dari tenant lain | RLS PostgreSQL 18 | Diblokir oleh policy RLS dengan respons `404 NOT_FOUND`. |
| 25 | Staf dengan role barista/kitchen mencoba membuka shift atau mencatat kas | Otorisasi Negatif | Ditolak dengan `403 FORBIDDEN`. |
| 26 | Upaya manipulasi mutasi kas melalui UPDATE atau DELETE | Integritas Basis Data | Trigger anti-tampering membangkitkan exception PostgreSQL dan membatalkan transaksi. |
| 27 | Koneksi terputus saat request pembayaran kas dikirimkan | Jaringan & Retries | Idempotent replay mengembalikan response sukses tanpa duplikasi mutasi kas. |
| 28 | Kontrak pengembalian kas (*cash refund*) untuk Iteration 4d | Forward Compatibility | Menolak refund tunai jika nilai refund melebihi sisa `expected_cash` di laci kas. |
| 29 | Kegagalan printer fisik saat menerima sinyal `open_drawer: true` | Hardware Resilience | Kegagalan hardware printer tidak boleh membatalkan pembayaran yang sudah tercatat di basis data. |
| 30 | Server crash / restart mendadak saat shift kasir aktif | Disaster Recovery | Status shift dan seluruh riwayat mutasi kas tetap utuh saat server menyala kembali. |
