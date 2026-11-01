# MyCaffe Go Style, Naming Conventions & Anti-AI-Slop Standard

Dokumen ini menetapkan standar konvensi penamaan global, arsitektur kode Go yang idiomatik, dan kebijakan lokalisasi (i18n) untuk repositori `mycaffe-backend`. Standar ini menjadi acuan mutlak dalam seluruh proses *code review* dan refactoring.

---

## 1. Global Naming Conventions

### 1.1 Identifier Go (Tanpa Underscore `_` dan Tanpa Hyphen `-`)
Sesuai panduan resmi [Effective Go](https://go.dev/doc/effective_go) dan [CodeReviewComments](https://go.dev/wiki/CodeReviewComments):
- **Exported Identifiers (Struct, Interface, Function, Method, Constant, Field)**: Wajib menggunakan `PascalCase` (`UpperCamelCase`).
  - Contoh benar: `CatalogService`, `ModifierGroup`, `PriceDelta`, `CashDrawer`, `OpenShiftInput`.
  - Contoh salah: `Catalog_Service`, `catalog-service`, `price_delta`, `modifier_group`.
- **Unexported Identifiers (Internal Function, Method, Field, Local Variable)**: Wajib menggunakan `camelCase` (`lowerCamelCase`).
  - Contoh benar: `catalogView`, `minSelect`, `orderRepo`, `currentStaff`.
  - Contoh salah: `catalog_view`, `min_select`, `order_repo`.
- **Pengecualian Karakter Underscore `_`**:
  - Hanya diizinkan untuk *blank identifier* `_` (misalnya mengabaikan *return value* atau *import for side-effect*).
  - Tidak boleh ada variabel seperti `user_id`, `item_id`, atau `group_id` di dalam kode Go.

### 1.2 Initialisms Kanonikal (Standar Singkatan Go)
Singkatan teknis harus konsisten secara huruf kapital penuh atau kecil penuh:

| Singkatan | Bentuk Exported Benar | Bentuk Unexported Benar | Bentuk Salah (Dilarang) |
|-----------|----------------------|-------------------------|-------------------------|
| Identifier | `ID`, `StoreID`, `UserID` | `id`, `storeID`, `userID` | `Id`, `StoreId`, `UserId` |
| URL | `URL`, `CallbackURL` | `url`, `callbackURL` | `Url`, `CallbackUrl` |
| HTTP | `HTTPClient`, `HTTPTransport` | `httpClient` | `HttpClient` |
| API | `APIRoute`, `OpenAPI` | `apiRoute` | `ApiRoute` |
| Point of Sale | `POSOrder`, `POSQuote` | `posOrder` | `PosOrder`, `PosQuote` |
| PIN | `PINHash`, `OwnerPIN` | `pinHash`, `ownerPIN` | `PinHash`, `OwnerPin` |
| QRIS | `QRISDetails`, `QRISMDRBPS` | `qrisDetails` | `QrisDetails`, `QrisMdrBps` |
| RLS | `RLSPolicy` | `rlsPolicy` | `RlsPolicy` |
| UUID | `UUID` | `uuid` | `Uuid` |
| Basis Points | `BPS`, `TaxBPS` | `bps`, `taxBPS` | `Bps`, `TaxBps` |

### 1.3 Penamaan File Go
- Nama file Go wajib menggunakan format `snake_case.go` lowercase.
- Dilarang menggunakan hyphen `-` pada nama file Go (karena tooling `go build` dan cross-compilation dapat menginterpretasikan hyphen sebagai filter arsitektur).
- Nama file harus mencerminkan kapabilitas fungsional (*capability-based*), bukan riwayat pekerjaan agent atau nomor iterasi.
  - Contoh benar: `order_create.go`, `order_quote.go`, `catalog_modifier.go`, `shift_service.go`.
  - Contoh salah: `iteration_4a_test.go`, `agent_refactor_v2.go`, `catalog-modifier.go`.

### 1.4 Penamaan Database & JSON
- **Database (Tabel, Kolom, Constraint, Index)**: Wajib menggunakan `snake_case` dalam Bahasa Inggris.
  - Tabel: `modifier_groups`, `cash_movements`, `store_settings`.
  - Kolom: `price_delta`, `created_at`, `expected_cash`.
- **JSON Payload (Request / Response Contract)**: Wajib menggunakan `snake_case` dalam Bahasa Inggris.
  - Field: `{"price_delta": 5000, "quote_fingerprint": "abc...", "open_drawer": true}`.
- **REST URL Path**: Wajib menggunakan `kebab-case` lowercase.
  - Path: `/api/v1/catalog/modifier-groups`, `/api/v1/pos/shifts/x-report`.

---

## 2. Kebijakan Bahasa & Lokalisasi (i18n & Localization)

Sistem MyCaffe memisahkan secara tegas domain komputasi dengan representasi pengguna:

| Domain | Bahasa yang Digunakan | Penjelasan & Contoh |
|--------|----------------------|---------------------|
| **Identifier Kode Go** | **Bahasa Inggris** | Nama struct, method, interface, variable, constant: `OrderService`, `CalculateExpectedCash`, `ErrShiftNotOpen`. |
| **Database Schema** | **Bahasa Inggris** | Nama tabel, kolom, constraints, RLS policies: `shifts`, `counted_cash`, `chk_shifts_status`. |
| **Error Code (Kanonikal)** | **Bahasa Inggris (SCREAMING_SNAKE_CASE)** | Machine-readable error code: `QUOTE_CHANGED`, `NO_OPEN_SHIFT`, `VARIANCE_EXCEEDS_TOLERANCE`, `INVALID_OWNER_PIN`. |
| **Log Server & Telemetri** | **Bahasa Inggris** | System log, audit action, Sentry breadcrumbs: `"failed to insert cash movement"`, `"payment settlement initiated"`. |
| **User-Facing Copy & Error Messages** | **Bahasa Indonesia Natural** | Pesan error dan notifikasi yang ditampilkan ke staf kasir, barista, atau pelanggan: `"Data tidak ditemukan."`, `"Grup modifier masih terhubung ke menu."`, `"Selisih kas melebihi toleransi toko, otorisasi PIN Owner diperlukan."` |

---

## 3. Aturan Anti-AI-Slop (Clean Go Monolith)

1. **No Interface Bloat (Interface Segregation)**:
   - Jangan membuat antarmuka (`interface`) untuk setiap struct konkret. Di Go, antarmuka didefinisikan oleh konsumen (*consumer-defined*), bukan oleh produsen. Buat interface hanya ketika ada kebutuhan polimorfisme nyata (misalnya Midtrans Gateway Client mockable, System Clock mockable).
2. **No Shallow Passthrough Layers**:
   - Dilarang membuat service wrapper yang hanya memanggil satu baris repository tanpa menambah validasi bisnis, otorisasi, atau transaksi.
3. **No Duplicate Financial Validation**:
   - Aturan finansial (kalkulasi pajak, pembulatan BPS, expected cash, selisih kas) hanya dihitung di satu tempat (Service Domain). Handler HTTP dilarang menduplikasi kalkulasi matematika uang.
4. **No Speculative Dependency Injection Frameworks**:
   - Hindari framework DI berat seperti Uber Digi atau Wire jika *manual constructor injection* (`NewCatalogService(repo, tx)`) sudah cukup jelas dan dapat ditelusuri (*grep-friendly*).
5. **No Blind Global Mutability**:
   - Seluruh dependensi (clock, repository, mailer, gateway) diinjeksikan secara eksplisit via struct fields; dilarang menggunakan variabel global mutabel untuk state aplikasi.
6. **No Comment Slop**:
   - Dilarang menulis komentar deskriptif panjang yang hanya mengulangi nama fungsi (misal: `// GetModifierGroup gets the modifier group`). Komentar hanya digunakan untuk mendokumentasikan *invariants*, alasan arsitektural (*why*), dan batasan konkurensi.
