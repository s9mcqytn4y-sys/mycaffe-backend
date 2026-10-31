# MyCaffe Core POS: Review Use Cases, Sequence Diagrams, dan Alur Operasional End-to-End

Dokumen ini mendefinisikan use case operasional, diagram sekuens (sequence diagrams), aturan akuntabilitas kas, siklus hidup antrean, keranjang (cart), pemisahan dine-in vs takeaway, serta analitik data pemilik usaha pada **MyCaffe Core POS**.

---

## 1. Pemisahan Tegas Siklus Hidup Pesanan, Pembayaran, dan Reservasi Stok

Setiap pesanan pada MyCaffe dikelola oleh tiga state machine ortogonal yang dijaga oleh constraint PostgreSQL 18 (`CHECK` constraints):

| Dimensi State | Kolom Tabel | Nilai Valid | Tujuan Operasional |
| :--- | :--- | :--- | :--- |
| **Order Preparation Status** | `orders.status` | `pending_payment`, `queued`, `preparing`, `ready`, `served`, `completed`, `cancelled` | Mengatur visibilitas tiket pada KDS Barista/Dapur dan layar TV Queue Display. |
| **Order Payment Status** | `orders.payment_status` | `unpaid`, `pending`, `partially_paid`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback` | Mengatur status pelunasan tagihan pesanan secara finansial. |
| **Stock Reservation Status** | `orders.stock_status` | `not_tracked`, `reserved`, `committed`, `released` | Mencegah *overselling* saat pelanggan masih berada di layar pembayaran QRIS tanpa menyebabkan kebocoran stok permanen (*stock leak*) jika pembayaran kedaluwarsa. |

```mermaid
stateDiagram-v2
    [*] --> PendingPayment_Reserved: CreateOrder (pay_first, track_stock=true, expires_at=+15m)
    [*] --> Queued_Committed: CreateOrder (open_tab oleh Cashier/Owner aktif)
    PendingPayment_Reserved --> Queued_Committed: ConfirmOrderPaymentAndCommitStock (Settlement / Tunai Lunas)
    PendingPayment_Reserved --> Cancelled_Released: CancelOrExpireUnpaidOrder (QRIS Expire / Batal, stok dikembalikan 1x)
    Cancelled_Released --> Queued_Committed: Late Settlement Webhook (Re-deduct stok atomik jika masih cukup)
    Queued_Committed --> Preparing_Committed: Barista/Dapur mulai meracik
    Preparing_Committed --> Ready_Committed: Minuman/makanan selesai diracik (Panggil di TV Queue)
    Ready_Committed --> Served_Committed: Diantar ke meja / diambil pelanggan
    Served_Committed --> Completed_Committed: Transaksi ditutup
```

---

## 2. Cart System, Katalog Menu, dan 1-Level Nested Sub-Addons

### 2.1 Aturan Keranjang (Client Cart vs Server Pricing Authority)
1. **Keranjang di Browser/PWA Hanya Bersifat Tampilan Sementara:**
   - Aplikasi Web/PWA menyimpan pilihan produk, opsi modifier, sub-addon, dan catatan item di memori lokal perangkat pelanggan disertai `idempotency_key` (UUID v4) yang dibangkitkan saat pelanggan menekan tombol **Bayar / Buat Pesanan**.
   - Server Go **tidak pernah** mempercayai harga dasar, nama produk, harga modifier, diskon, pajak, atau total tagihan yang dikirim dari browser.
2. **Deduplikasi Baris Item (`config_hash`):**
   - Dua item dengan `product_id` yang sama tetapi kombinasi modifier/sub-addon berbeda menghasilkan `config_hash` SHA-256 yang berbeda sehingga tercatat sebagai baris `order_items` terpisah dengan rincian `order_item_options` masing-masing.
3. **Validasi Modifier & 1-Level Sub-Addon (`ValidateAndPriceOrder`):**
   - Grup modifier bertipe `single` wajib memiliki `quantity == 1`. Pengiriman `quantity > 1` pada grup `single` ditolak dengan `ErrInvalidModifierSelection`.
   - Jika opsi induk pada grup `multiple` memiliki `quantity > 1` dan membuka grup sub-addon anak (`option_modifier_groups`), maka biaya dan kebutuhan stok sub-addon dihitung secara proporsional:
     $$\text{Total Kebutuhan Sub-Addon} = \text{ItemQty} \times \text{ParentModQty} \times \text{SubAddonQty}$$

---

## 3. Alur Dine-In (QR Meja & Open Tab) vs Takeaway (Express Counter)

| Aspek Operasional | Dine-In (`pay_first` via QR Meja) | Dine-In (`open_tab` via Kasir/Pelayan) | Takeaway (`pay_first` di Kasir / QR Counter) |
| :--- | :--- | :--- | :--- |
| **Kewajiban `table_number`** | Wajib diisi (diambil dari token tanda tangan QR meja). | Wajib diisi oleh kasir/staf. | Opsional (kosong); menggunakan `queue_number` (`A001`) sebagai identitas panggil. |
| **Otorisasi Pembuat** | Pelanggan publik (dengan signed table QR token) atau kasir. | Wajib staf aktif (`owner` atau `cashier`) yang terverifikasi di `users`. | Pelanggan publik atau kasir. |
| **Status Awal Pesanan** | `status = pending_payment`, `payment_status = unpaid`, `stock_status = reserved`. | `status = queued`, `payment_status = unpaid`, `stock_status = committed`. | `status = pending_payment`, `payment_status = unpaid`, `stock_status = reserved`. |
| **Kapan Masuk ke KDS?** | Segera setelah pembayaran terverifikasi (`payment_status = paid`). | Langsung masuk ke KDS saat pesanan dibuat (`queued`). | Segera setelah pembayaran terverifikasi (`payment_status = paid`). |

### 3.1 Sequence Diagram: QR Table Ordering (`pay_first` dengan Dynamic QRIS Midtrans)

```mermaid
sequenceDiagram
    autonumber
    actor Cust as Pelanggan (Meja 04)
    participant PWA as Web/PWA Table Order
    participant API as Go Backend (Order & Payment Service)
    participant DB as PostgreSQL 18 (RLS + ACID)
    participant Mid as Midtrans Core API
    participant KDS as KDS Barista & TV Queue

    Cust->>PWA: Pilih menu + modifier + sub-addon, klik "Bayar QRIS"
    PWA->>API: POST /api/v1/orders (idempotency_key, items)
    API->>DB: BEGIN + SET LOCAL app.current_store_id
    API->>DB: Lock products & modifier_options FOR UPDATE (sorted UUID)
    API->>DB: Deduct stock, allocate store_daily_sequences (ORD-YYYYMMDD-XXXX, A001)
    API->>DB: INSERT orders (pending_payment, unpaid, stock_status=reserved, expires_at=+15m)
    API->>DB: INSERT order_items, order_item_options, payment_splits (Main Bill), outbox_events
    API->>DB: COMMIT
    API-->>PWA: 201 Created (order_id, split_id, total_amount=Rp 83.160)

    PWA->>API: POST /api/v1/orders/{id}/payments/initiate (method=qris, split_id)
    API->>DB: INSERT payments (status=pending, provider_order_id=ORD-...-S1-A1)
    API->>Mid: POST /v2/charge (payment_type=qris, gross_amount=83160)
    Mid-->>API: 201 Created (transaction_id, qr_string, actions, expiry_time)
    API->>DB: UPDATE payments (provider_transaction_id, qr_string, expires_at)
    API-->>PWA: 201 Created (qr_string, expiry_time)

    Cust->>Mid: Scan & bayar QRIS via aplikasi bank/e-wallet
    Mid->>API: POST /api/v1/payments/midtrans/webhook (settlement, signature_key)
    API->>API: Verifikasi SHA-512 signature & integer IDR gross_amount
    API->>DB: BEGIN + Lookup tenant via payment_webhook_routing
    API->>DB: INSERT payment_events (ON CONFLICT DO NOTHING)
    API->>DB: UPDATE payments -> paid, payment_splits -> paid
    API->>DB: ConfirmOrderPaymentAndCommitStock (orders: paid, queued, stock_status=committed)
    API->>DB: INSERT outbox_events (order.paid) + COMMIT
    API-->>Mid: 200 OK
    API--)KDS: WebSocket Broadcast (order.paid -> tampil di KDS Barista & TV Queue)
    API--)PWA: WebSocket Update (Status: Antrean A001 - Sedang Disiapkan)
```

---

## 4. Pembayaran Tunai, QRIS Statis, EDC, Akuntabilitas Kasir, dan Dual-Screen POS

### 4.1 Aturan Akuntabilitas Pembayaran Tunai & Kasir (`cash`, `static_qris`, `edc`)
1. **Otorisasi Ketat:**
   - Metode `cash`, `static_qris`, dan `edc` **hanya** dapat dieksekusi oleh staf terautentikasi dengan peran `cashier` atau `owner` pada toko tersebut. Pelanggan pada layar QR meja tidak dapat menandai pesanan mereka sendiri sebagai lunas tunai.
2. **Validasi Uang Diterima & Kembalian (`cash`):**
   - Kasir memasukkan nominal uang fisik yang diterima (`cash_tendered_amount`).
   - Backend memvalidasi `cash_tendered_amount >= split.total_amount`.
   - Kembalian dihitung di server: `change_given_amount = cash_tendered_amount - split.total_amount`.
   - Seluruh angka (`amount`, `cash_tendered_amount`, `change_given_amount`, dan `received_by_user_id`) disimpan secara permanen pada baris `payments` dan dicatat di `audit_logs`.
3. **Layar Kedua Pelanggan (Dual-Screen Cashier Display):**
   - Saat kasir menambahkan item atau memilih metode pembayaran di layar utama POS, layar kedua yang menghadap pelanggan menampilkan rincian item, modifier, pajak PB1, service charge, total tagihan (`tabular-nums`), kode QRIS (jika memilih QRIS), serta nominal uang tunai yang diterima dan kembalian secara real-time.
4. **Fleksibilitas Shift Kasir & Arus Kas (`Cashflow`):**
   - Pada mode `single_auto`, seluruh transaksi tunai harian terakumulasi otomatis pada buku kas tanggal bisnis toko (`stores.timezone`), memisahkan:
     - **Kas Masuk Penjualan Tunai:** `SUM(payments.amount) WHERE method = 'cash' AND status = 'paid'`.
     - **Penerimaan Non-Tunai Terverifikasi Gateway:** `qris`, `gopay`, `shopeepay`, `bank_transfer` (dikurangi estimasi MDR per metode untuk proyeksi pencairan bersih / *net settlement*).
     - **Penerimaan Non-Tunai Rekonsiliasi Kasir:** `static_qris` dan `edc` (wajib dicocokkan dengan struk settlement EDC/mutasi statis saat penutupan buku).
   - Pada mode `multi_shift`, setiap kasir mencatat *Opening Cash Float* (modal laci awal), kas masuk/keluar operasional (*petty cash drop/payout* dengan alasan wajib), dan *Closing Physical Cash Count* untuk mendeteksi selisih kas (*variance/discrepancy*) secara instan.

---

## 5. Kitchen Display System (KDS), Sistem Antrean TV, dan Pelacakan Pelanggan Real-Time

```mermaid
sequenceDiagram
    autonumber
    actor Barista as Barista / Kitchen (KDS)
    participant API as Go Backend
    participant DB as PostgreSQL 18
    participant TV as TV Queue Display
    participant CustPWA as Layar Pelanggan (Live Tracker)

    Note over Barista,CustPWA: Pesanan ORD-20261030-0012 (Antrean A012) berstatus "queued"
    Barista->>API: PATCH /api/v1/kds/orders/{id}/status (status = "preparing")
    API->>DB: UPDATE orders SET status='preparing' + INSERT outbox_events
    API--)TV: WS Event: A012 pindah ke kolom "Sedang Disiapkan"
    API--)CustPWA: WS Event: Toast & Status "Pesanan A012 sedang diracik barista"

    Barista->>API: PATCH /api/v1/kds/orders/{id}/status (status = "ready")
    API->>DB: UPDATE orders SET status='ready' + INSERT outbox_events
    API--)TV: WS Event: A012 pindah ke "Siap Diambil / Diantar" + Panggilan Suara
    API--)CustPWA: WS Event: "Pesanan A012 sudah siap!"

    Barista->>API: PATCH /api/v1/kds/orders/{id}/status (status = "served")
    API->>DB: UPDATE orders SET status='served' + INSERT audit_logs
    API--)TV: WS Event: Hapus A012 dari layar panggilan aktif
    API--)CustPWA: WS Event: Tampilkan rincian pesanan telah disajikan & unduh E-Receipt
```

---

## 6. Data Analitik & Pemantauan Bisnis untuk Pemilik (Owner Analytics)

Seluruh laporan analitik pemilik dihitung dari data snapshot permanen (`orders`, `order_items`, `order_item_options`, `payments`) di dalam transaksi tenant (`store_id`) menggunakan zona waktu lokal toko (`stores.timezone`):

1. **Ringkasan Penjualan & Pendapatan Bersih (Revenue Breakdown):**
   - *Gross Sales* (`SUM(subtotal_amount)`), *Total Discount*, *Net Sales*, *Service Charge Collected*, *PB1 Tax Collected*, dan *Total Gross Collected*.
   - Estimasi biaya MDR gateway (`stores.qris_mdr_bps`) dan proyeksi dana cair bersih (*Estimated Net Payout*).
2. **Rekonsiliasi per Metode Pembayaran:**
   - Pemisahan volume dan nilai transaksi antara `qris` (dinamis Midtrans), `gopay`, `shopeepay`, `bank_transfer`, `cash`, `static_qris`, dan `edc`.
3. **Performa Menu, Modifier, dan Jam Sibuk:**
   - Produk terlaris berdasarkan kuantitas dan kontribusi pendapatan (`order_items.snapshot_name`).
   - Opsi modifier dan sub-addon paling populer (mis. persentase pelanggan yang memilih *Oat Milk Barista Blend*).
   - Distribusi kepadatan pesanan per jam (*hourly order velocity*) dan rata-rata waktu penyelesaian KDS (*queued-to-ready SLA duration*).
4. **Audit Keamanan Operasional:**
   - Daftar lengkap pesanan yang dibatalkan/kedaluwarsa (`stock_status = 'released'`), pengembalian dana (`refunded`), perubahan harga katalog, dan selisih kas shift dari tabel `audit_logs`.
