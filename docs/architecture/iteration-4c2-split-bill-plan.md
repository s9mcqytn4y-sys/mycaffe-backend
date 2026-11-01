# Technical Execution Plan: Iteration 4c-2 (Split Bill & Tab Settlement Lifecycle)

- **Branch Target**: `feature/iteration-4c2-split-bill`
- **Scope**: Split Bill Engine (Equal Split, By Item, Custom Amount), Multi-Tender Settlement, Shift Reconciliation, dan Tab Closure Lifecycle.
- **Prerequisite**: PR #20 (`feature/iteration-4c-open-tab-rounds`) merged to `staging`.

---

## 1. Business & Technical Architecture

### A. Tiga Strategi Split Bill
1. **Equal Split (Bagi Rata $N$ Orang)**:
   - Total tagihan dibagi menjadi $N$ split identik: `base_amount = floor(total_amount / N)`.
   - **Deterministic Remainder Allocation**: Selisih sisa pembagian bulat integer Rupiah `total_amount - (base_amount * N)` ditambahkan secara eksklusif ke **Split #1**.
   - Contoh: Tagihan Rp100.000 dibagi 3 orang:
     - Split #1: Rp33.334
     - Split #2: Rp33.333
     - Split #3: Rp33.333
     - Total: Rp100.000 (Presisi integer Rp 100%).
2. **Split by Item (Berdasarkan Pesanan per Tamu)**:
   - Kasir memilih subset item dan kuantitas per split.
   - Subtotal per split dihitung dari item pilihan. Pajak dan service charge dihitung proporsional atau dihitung ulang per split dengan aturan: selisih pembulatan BPS dialokasikan ke **Last Split** (split terakhir).
3. **Custom Amount Split (Nominal Fleksibel)**:
   - Kasir/tamu menentukan jumlah pembayaran bebas (misal Tamu A bayar Rp50.000, sisanya Tamu B).
   - Validasi ketat server-side: $\sum \text{Split Amount} = \text{Total Tagihan}$.

---

### B. Payment Splits & Lifecycle Database Schema
Tabel `payment_splits` telah dipersiapkan sejak Iteration 1 dan 4a. Pada Iteration 4c-2, kita mengaktifkan integrasi penuh:
- Kolom `status`: `unpaid` $\to$ `paid` $\to$ `refunded`.
- Relasi `payments.split_id` $\to$ mengikat transaksi pembayaran (Cash, Static QRIS, Midtrans Dynamic QRIS, EDC) ke entri split terkait.
- Relasi `payments.shift_id` $\to$ mengikat pembayaran ke shift kasir yang sedang aktif saat pelunasan dilakukan.

---

### C. Tab Closure & Shift Decoupling
1. **Tab Tidak Terikat Shift**:
   - Tab yang dibuka pada shift siang dapat dilunasi pada shift malam.
   - Pembayaran tunai/non-tunai dicatat pada sesi shift yang aktif saat pembayaran dieksekusi.
2. **Tab Closure Rule**:
   - Order dengan `billing_mode = 'open_tab'` HANYA bertransisi status:
     - `status = 'completed'` saat `paid_amount == total_amount` (lunas 100%).
     - Tidak boleh ada status tab "sebagian lunas" di luar pelacakan status per-split.
3. **Shift X/Z Report Warning**:
   - Saat kasir melakukan `POST /pos/shifts/current/close`, sistem melakukan query terhadap tab yang masih terbuka (`status = 'open' AND store_id = current_store_id()`).
   - Laporan penutupan shift memuat ringkasan peringatan: `unsettled_open_tabs_count` dan daftar `order_number` terkait agar kasir & supervisor waspada terhadap tagihan yang belum lunas.

---

## 2. API Contract Specification (PR 4c-2)

1. `POST /api/v1/pos/tabs/:id/splits` (Permission: `tab.manage`):
   - Menerima konfigurasi split (`strategy`: `equal`, `by_item`, `custom`).
   - Mengembalikan daftar split bill dengan nominal presisi.
2. `POST /api/v1/pos/tabs/:id/splits/:split_id/pay` (Permission: `payment.create`):
   - Mengeksekusi tender pembayaran (Cash, QRIS, EDC) untuk split tertentu.
   - Jika tender Cash, otomatis memotong/menambah kas laci dan mencatat movement `SHIFT_TENDER`.
3. `POST /api/v1/pos/tabs/:id/close` (Permission: `tab.manage`):
   - Menutup tab setelah 100% lunas. Menghasilkan final consolidated receipt snapshot.

---

## 3. Testing Strategy (Integration Gates)
- Test 1: Equal split 3 orang dengan pembulatan ganjil memverifikasi sisa Rupiah masuk ke Split 1.
- Test 2: Split by item memverifikasi alokasi item akurat dan residual pembulatan masuk ke split terakhir.
- Test 3: Pembayaran sebagian split menjaga tab tetap `open`; pelunasan split terakhir otomatis mengubah status tab menjadi `completed`.
- Test 4: Kasir shift B melunasi tab yang dibuka oleh kasir shift A; kas masuk ke shift B.
- Test 5: Idempotency pada pembayaran split mencegah double-charge dan double cash drawer movement.
