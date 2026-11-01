# Codebase Inventory, Cohesion & Modularization Audit

Dokumen ini menyajikan inventarisasi lengkap source code Go pada repositori `mycaffe-backend`, menganalisis kohesi dan tanggung jawab file berukuran besar (*God files*), serta merinci rencana refactoring modular untuk mencegah kode spageti menjelang implementasi Iteration 4b.

---

## 1. Peringkat Ukuran File & Analisis Kohesi

Berdasarkan inventarisasi AST dan baris kode aktual pada commit `316cfa9`:

| Peringkat | File Path | Baris | Fungsi | Tanggung Jawab Utama | Tingkat Kohesi | Risiko Arsitektur | Tindakan Rekomendasi |
|-----------|-----------|-------|--------|----------------------|----------------|-------------------|----------------------|
| 1 | `internal/service/catalog_service.go` | 1.123 | 36 | CRUD Kategori, Produk, Varian, Modifier Groups, Modifier Options, dan Sub-Addon Linking | **Low** (Terlalu banyak domain aggregate tercampur) | **High** (Perubahan modifier berisiko merusak katalog produk dasar) | **Split** menjadi beberapa file dalam package yang sama (PR 4b-0) |
| 2 | `internal/service/order_service.go` | 857 | 15 | Siklus Order (Create, Cancel, Payment Confirm, Quote API, Stock Reservation, Fingerprint) | **Medium-Low** | **High** (Mencampur quoting stateless dengan mutasi order stateful) | **Split** memisahkan quote engine dan lifecycle order |
| 3 | `internal/service/payment_service_integration_test.go` | 785 | 18 | Integrasi payment ledger, settlement, dan Midtrans webhook | **Medium** | **Medium** (Test suite terlalu panjang dan sulit dinavigasi) | **Consolidate** pisahkan berdasarkan capability pembayaran |
| 4 | `internal/service/pricing.go` | 715 | 10 | Mesin kalkulasi murni: BPS rounding, line pricing, sub-addon pricing, hash request | **High** (Pure functional) | **Low** (Kohesif, murni komputasi matematika IDR) | **Keep** sebagai domain pricing engine terpusat |
| 5 | `internal/repository/postgres/order_repository.go` | 711 | 26 | SQL untuk Order, Item, Modifier Option Snapshots, Stock Deduction, Splits, Daily Sequence | **Medium-Low** | **High** (Penyatuan SQL mutasi order dan loading katalog quote) | **Split** pisahkan query load catalog quote dari order persistence |
| 6 | `internal/service/order_service_integration_test.go` | 683 | 12 | Integrasi order lifecycle, stock lock, dan daily sequences | **Medium** | **Medium** | **Consolidate** bagi ke file spesifik |
| 7 | `internal/repository/postgres/payment_repository.go` | 599 | 22 | Payment transactions, ledger idempotency, duplicate checks | **High** (Fokus pada ledger) | **Medium** | **Keep** (Telah dirapikan di Iteration 3) |
| 8 | `internal/service/payment_initiate.go` | 539 | 8 | Inisiasi pembayaran POS & Gateway | **High** | **Low** | **Keep** |
| 9 | `internal/service/auth_service.go` | 531 | 16 | Login, refresh tokens, RLS resolver, PIN verification | **High** | **Low** | **Keep** |
| 10 | `internal/handler/iteration_4a_integration_test.go` | 495 | 10 | HTTP integration tests untuk Iteration 4a | **Medium** | **Medium** (Nama terikat riwayat iterasi, bukan fitur) | **Rename** menjadi `catalog_modifier_integration_test.go` |
| 11 | `internal/service/staff_lifecycle.go` | 467 | 14 | Manajemen staf, undangan, aktivasi akun, PIN reset | **High** | **Low** | **Keep** |
| 12 | `internal/repository/postgres/catalog_repository.go` | 442 | 39 | Query SQL Kategori, Produk, Varian, Modifiers | **Medium** | **Medium** | **Split** pisahkan modifier repository |
| 13 | `internal/service/pricing_test.go` | 436 | 12 | Unit tests murni untuk pricing dan BPS math | **High** | **Low** | **Keep** |
| 14 | `internal/handler/catalog_handler.go` | 410 | 18 | HTTP endpoints untuk seluruh katalog dan modifiers | **Medium** | **Medium** | **Split** pisahkan modifier handler |
| 15 | `internal/config/config.go` | 406 | 12 | Parsing konfigurasi environment dan fallback defaults | **High** | **Low** | **Keep** |

---

## 2. Temuan Code Smell & Potensi Spaghetti Code

### 2.1 Monolithic Catalog Service (`catalog_service.go` — 1.123 baris)
- **Masalah**: Satu file menangani 4 aggregate root yang berbeda: Kategori, Produk, Varian, dan Graf Modifiers (Modifier Group, Modifier Option, Sub-Addon Depth-1 Linking).
- **Dampak**: Navigasi kode sulit, merge conflict tinggi jika beberapa fitur katalog dikembangkan bersamaan, dan resiko regresi logika saat mengubah relasi modifier.
- **Rekomendasi Pemisahan**:
  - `catalog_service.go`: Struct utama `CatalogService`, constructor `NewCatalogService`, helper transaksi, dan pembacaan katalog lengkap (`StaffCatalog`, `PublicCatalog`).
  - `catalog_category.go`: Method CRUD kategori (`CreateCategory`, `UpdateCategory`, `DeleteCategory`, `applyCategory`).
  - `catalog_product.go`: Method CRUD produk dan varian (`CreateProduct`, `UpdateProduct`, `ArchiveProduct`, `SetAvailability`, `CreateVariant`, `UpdateVariant`, `ArchiveVariant`).
  - `catalog_modifier.go`: Method CRUD modifier groups, options, sub-addon child linking, validasi kedalaman graf, dan audit perubahan harga (`CreateModifierGroup`, `UpdateModifierGroup`, `ArchiveModifierGroup`, `CreateModifierOption`, dsb.).

### 2.2 Pencampuran Quoting Stateless dan Mutasi Transaksional Order (`order_service.go` — 857 baris)
- **Masalah**: File ini mencampur pembuatan pesanan transaksional yang mengubah stok fisik (`CreateOrder`, `CancelOrExpireUnpaidOrder`, `ConfirmOrderPaymentAndCommitStock`) dengan kalkulasi quote stateless (`QuoteOrder`, `buildQuoteResult`, `ComputeQuoteFingerprint`).
- **Rekomendasi Pemisahan**:
  - `order_service.go`: Struct `OrderService`, constructor, dan transisi status order (`CancelOrExpireUnpaidOrder`, `ConfirmOrderPaymentAndCommitStock`).
  - `order_create.go`: Alur transaksional order (`CreateOrder`, `executeCreateOrderTx`, kalkulasi stok baris pesanan).
  - `order_quote.go`: Mesin quotation POS & Publik, kalkulasi fingerprint SHA-256 (`QuoteOrder`, `ComputeQuoteFingerprint`, `buildQuoteResult`).

### 2.3 SQL Order Repository Memuat Query Katalog Quote (`order_repository.go` — 711 baris)
- **Masalah**: `OrderRepository` memuat method `LockAndLoadCatalogForOrder` dan `LoadCatalogForQuote` yang membaca tabel katalog (`categories`, `products`, `variants`, `modifier_groups`, `modifier_options`).
- **Evaluasi Arsitektural**: Hal ini dilakukan untuk menjaga transaksi pemesanan berjalan dalam 1 query roundtrip terpadu dengan row-lock `FOR UPDATE` terhadap opsi modifier dan produk. Pemisahan query ini tetap harus berada di bawah repository PostgreSQL yang kohesif tanpa menciptakan siklus dependensi antar-repository.

---

## 3. Evaluasi Keterikatan Antarservice (Service Coupling)

Menjelang implementasi **Iteration 4b (Shift & Cash Drawer)**:
1. **Risiko Cyclic Import / Direct Dependency**:
   - `PaymentService` membutuhkan pencatatan kas masuk ke laci kas saat menerima pembayaran tunai (*cash tender*).
   - `ShiftService` membutuhkan ringkasan pembayaran tunai dan non-tunai saat mencetak laporan X dan Z.
   - **Solusi Anti-Spageti**:
     - DILARANG membuat `PaymentService` memanggil `ShiftService`, lalu `ShiftService` memanggil `PaymentService`.
     - Gunakan pendekatan **Shared Transaction Repository Boundary**:
       - `PaymentService` membuka transaksi tenant (`database.WithTenantTx`).
       - Memeriksa shift kasir aktif via `shiftRepo.GetOpenShiftForUpdate(ctx, tx, storeID, drawerID)`.
       - Mencatat pembayaran ke tabel `payments`.
       - Mencatat pergerakan kas ke tabel `cash_movements` (tipe `sale`, nilai rupiah riil yang dibayarkan).
       - Meng-commit transaksi secara atomik.
       - `ShiftService` bertindak secara independen untuk mengelola siklus shift kasir (buka shift, tutup shift, blind count, dan pembacaan laporan X/Z) tanpa perlu menginisiasi pembayaran.

---

## 4. Rencana Kerja Refactoring (PR 4b-0)

Pekerjaan modularisasi akan dilakukan secara mekanis pada PR terpisah (**PR 4b-0**) sebelum migrasi 000010 dibuat:
- **Scope**: Memecah `catalog_service.go` dan `order_service.go` menjadi file-file kapabilitas dalam package yang sama (`package service`).
- **Guarantees**:
  - Nol perubahan signature exported function / method (`CatalogService` API tetap 100% kompatibel).
  - Nol perubahan skema basis data.
  - Seluruh pengujian integration tests dan unit tests dijalankan sebelum dan sesudah pemecahan file.
  - Memastikan *git diff* bersih dan mudah ditinjau oleh reviewer.
