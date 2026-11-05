# Rencana Product Analytics

Status: draf untuk v0.9, dibahas bersama [PRD v0.9](PrdV09CafeFloor.md). Belum ada alat analytics pihak
ketiga; tahap pertama memakai data yang sudah ada di PostgreSQL.

## 1. Prinsip

1. **Server adalah sumber kebenaran.** Order, pembayaran, dan event outbox sudah tercatat append-only.
   Metrik produk diturunkan dari tabel itu, bukan dari klik di frontend yang bisa hilang atau dipalsukan.
2. **Tanpa PII.** Analytics tidak pernah menyimpan nama tamu, nomor HP, email, atau PIN. Identitas
   memakai `store_id`, `user_id` staf, dan `customer_id` (UUID).
3. **Per toko.** Semua metrik dihitung per `store_id` dan per hari bisnis toko (`stores.timezone`), sama
   dengan laporan owner (B2).
4. **Pisahkan operasional dan produk.** Prometheus (`mycaffe_*`) untuk kesehatan sistem; analytics produk
   untuk perilaku kafe. Keduanya tidak dicampur.

## 2. Taksonomi event

Format `objek.aksi` (huruf kecil, past tense), sama dengan event outbox yang sudah ada:
`order.created`, `order.paid`, `order.round_submitted`, `payment.partially_paid`,
`payment.split_settled`, `kds.ticket_created`, `kds.ticket_updated`, `approval.requested`,
`approval.decided`.

Event baru v0.9:

| Event | Pemicu | Properti wajib |
|-------|--------|----------------|
| `table.moved` | PR 9.1 pindah meja | `session_id`, `from_table_id`, `to_table_id`, `actor_user_id` |
| `table.merged` | PR 9.1 gabung | `primary_session_id`, `merged_session_id`, `actor_user_id` |
| `table.split` | PR 9.1 pisah | `primary_session_id`, `table_id`, `actor_user_id` |
| `table.timer_warning` / `table.timer_expired` | PR 9.2 worker | `session_id`, `table_id`, `ends_at` |
| `table.timer_extended` | PR 9.2 | `session_id`, `minutes`, `actor_user_id` |
| `queue.updated` | PR 9.3 | `queue_number`, `status` |
| `print_job.printed` / `print_job.failed` | PR 9.4 ack agen | `printer_id`, `kind`, `latency_ms`, `attempts` |
| `receipt.sent` | PR 9.5 | `order_id`, `channel` (`email`, `whatsapp`, `sms`) |

Properti umum di setiap event: `store_id`, `occurred_at` (UTC), `seq` (urutan outbox per toko).

## 3. Metrik

### 3.1 North Star

**Pesanan lunas per toko per hari bisnis** (`orders.payment_status = 'paid'`), dengan rata-rata waktu
dari `order.created` ke `order.paid`. Mengukur nilai inti: kafe menjual lebih banyak dengan antrean
yang lebih pendek.

### 3.2 KPI per fitur v0.9

| KPI | Definisi | Target awal |
|-----|----------|-------------|
| Adopsi aksi meja | `table.moved` + `table.merged` per 100 sesi meja | Baseline 2 minggu pertama |
| Void karena salah meja | Void dengan alasan berisi "meja" per 100 sesi | Turun ke 0 setelah 9.1 |
| Perputaran meja | Median `closed_at - opened_at` sesi per area (`tables.section`) | Baseline |
| Sesi lewat batas | `table.timer_expired` tanpa `table.timer_extended` / sesi bertimer | Baseline |
| Latensi cetak | p95 `print_job.printed.latency_ms` | ≤ 10 detik |
| Kegagalan cetak | `print_job.failed` / semua job | ≤ 1 % |
| Struk digital | `receipt.sent` / pesanan lunas, per kanal | Baseline |
| Waktu siap | Median `queued` → `ready` per stasiun (`kds_tickets`) | Baseline |

### 3.3 Metrik uang (sudah tersedia di laporan B2)

Penjualan kotor, diskon, pajak, service, refund, selisih kas, MDR. Tidak dihitung ulang di analytics;
analytics membaca laporan yang sama agar angka tidak pernah berbeda.

## 4. Implementasi bertahap

1. **v0.9: view SQL di database.** Satu view per KPI di §3.2, dijalankan sebagai `mycaffe_app` di dalam
   `WithTenantTx`, dengan endpoint owner `GET /reports/product?from=&to=`. Tidak ada tabel baru;
   sumbernya `outbox_events`, `audit_logs`, `orders`, `table_sessions`, `print_jobs`,
   `receipt_deliveries`.
2. **Retensi:** `outbox_events` dipurge oleh P0-08. Sebelum purge, agregat harian disimpan ke tabel
   `daily_product_metrics` (v0.10) agar riwayat tidak hilang.
3. **Alat eksternal (nanti):** jika owner butuh dashboard lintas toko, ekspor agregat harian (tanpa
   event mentah dan tanpa PII) ke alat pilihan owner. Keputusan alat menunggu klien kedua.

## 5. Kualitas data

- Setiap event baru wajib punya tes integrasi yang memastikan event terbit tepat sekali per aksi.
- Event dengan `store_id` kosong ditolak hub (aturan yang sudah ada di `internal/realtime/hub.go`).
- Perubahan nama atau properti event dicatat di dokumen ini dan di OpenAPI; event lama tidak diubah
  artinya.
