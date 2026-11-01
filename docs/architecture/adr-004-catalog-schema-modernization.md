# ADR-004: Catalog Schema Modernization & Variant-Driven Pricing

- **Status**: Proposed (Scheduled for Iteration 5 Catalog Refactor)
- **Deciders**: Principal Backend Engineer, PostgreSQL Architect, Product Owner
- **Date**: 2026-11-01

---

## 1. Context & Problem Statement

Pada arsitektur awal MyCaffe (Iteration 1–3), tabel `products` memiliki kolom `base_price` dan melacak stok (`track_stock`, `stock_quantity`) langsung pada level produk utama. Seiring diperkenalkannya multi-variant (`product_variants`) pada Iteration 3 dan nested modifier groups pada Iteration 4a:

1. **Dual Source of Truth Harga**: Terjadi duplikasi antara `products.base_price` dan `product_variants.price`. Jika suatu produk memiliki varian (misal Reguler / Large), `base_price` produk seringkali menjadi nilai dummy atau fallback yang rawan desinkronisasi.
2. **Keterbatasan Pelacakan Stok**: Pada kafe riil, stok bahan atau item sering kali terikat pada varian spesifik (misal cup 12oz vs 16oz, atau beans Single Origin tertentu). Melacak stok hanya di `products` tidak memungkinkan isolasi stok per varian.
3. **Inkonsistensi Naming Constraint Modifier**: Pengecekan bounds modifier selection sempat menggunakan nama kolom `min_selections` vs `min_select` pada beberapa dokumen perancangan awal.
4. **Semantik Status Katalog**: Perlunya pemisahan yang tegas antara produk yang sementara habis di kasir (`is_available = false`) dengan produk yang dipensiunkan dari menu (`archived_at IS NOT NULL`).

---

## 2. Decision Drivers

- **Zero Receipt Mutation**: Snapshot transaksi lama di `order_items` dan `order_item_options` tidak boleh terpengaruh oleh restrukturisasi katalog.
- **Single Source of Truth**: Satu produk harus selalu memiliki minimal satu varian default (`is_default = true`), dan harga dasar selalu dibaca dari varian yang dipilih.
- **Fail-Closed Financial Correctness**: Setiap perhitungan harga, diskon, dan pajak harus tetap berbasis integer Rupiah (`int64`/`BIGINT`) murni.
- **Multi-Tenant RLS Non-Owner Role**: Semua mutasi dan query wajib terisolasi per `store_id` di bawah runtime role `mycaffe_app` (`NOBYPASSRLS`).

---

## 3. Considered Options

### Opsi 1: Mempertahankan `products.base_price` sebagai fallback jika varian kosong
- *Kelebihan*: Kompatibilitas mundur dengan schema Iteration 1.
- *Kekurangan*: Kompleksitas logika di `pricing.go` (dua jalur kalkulasi harga), rawan bug inkonsistensi saat sinkronisasi frontend POS, dan mempersulit audit harga.

### Opsi 2 (Dipilih): Variant-Driven Pricing Murni (Menghapus `products.base_price`)
- *Kelebihan*:
  - Setiap produk otomatis memiliki minimal 1 default variant (`"Regular"` / `"Standar"`).
  - Harga item selalu `variant.price + sum(modifier_options.price_delta)`.
  - Struktur data bersih, deterministik, dan menyederhanakan validasi harga di `pricing.go`.
  - Memungkinkan pelacakan stok per varian (`product_variants.stock_quantity`, `product_variants.track_stock`).
- *Kekurangan*:
  - Membutuhkan migrasi data additive: backfill default variant untuk produk warisan sebelum mendrop kolom `base_price`.

---

## 4. Architectural Decision & Technical Design

### A. Catalog Schema Evolution (Additive Migration Plan)
1. **Backfill Default Variant**:
   Untuk setiap produk yang belum memiliki entri di `product_variants`, buat satu varian default:
   ```sql
   INSERT INTO product_variants (id, store_id, product_id, name, sku, price, is_default, track_stock, stock_quantity)
   SELECT uuid_generate_v4(), store_id, id, 'Regular', 'REG-' || SUBSTRING(id::text, 1, 8), base_price, true, track_stock, stock_quantity
   FROM products p
   WHERE NOT EXISTS (SELECT 1 FROM product_variants pv WHERE pv.product_id = p.id);
   ```
2. **Deprecate & Drop `products.base_price`**:
   Kolom `base_price` pada `products` di-drop setelah seluruh consumer kode diarahkan ke `product_variants.price`.
3. **Stock Delegation to Variants**:
   Pengecekan dan pengurangan stok atomik dialihkan dari `DeductProductStock` ke `DeductVariantStock(ctx, tx, storeID, variantID, qty)`.

### B. Modifier Constraint Standardization
Bounds constraint pada `modifier_groups` dikonsolidasikan secara universal:
```sql
ALTER TABLE modifier_groups 
ADD CONSTRAINT chk_modifier_groups_bounds 
CHECK (min_select >= 0 AND max_select >= 1 AND min_select <= max_select);
```

### C. Catalog Status Semantics
- **`is_available (BOOLEAN)`**: Status dinamis operasional kasir (misal susu oat habis hari ini -> di-toggle false oleh staff). Produk tetap tampil di menu dengan badge "Habis".
- **`archived_at (TIMESTAMPTZ)`**: Soft-delete administratif oleh store owner. Produk disembunyikan dari menu POS & QR customer, namun historical orders tetap merujuk UUID produk tanpa melanggar FK RESTRICT.

---

## 5. Consequences & Rollout

- **Positif**:
  - Model domain katalog menjadi sangat rapi dan konsisten dengan praktik terbaik POS modern.
  - Perhitungan quote dan order creation terpadu 100% pada varian.
  - POS dan QR table ordering mendapatkan pengalaman UI yang seragam untuk pemilihan varian dan modifier.
- **Mitigasi Risiko**:
  - Refactor ini dijadwalkan pada Iteration 5 setelah Milestone POS Core (Shift, Open Tab, dan Split Bill di Iteration 4c) selesai dan teruji secara penuh.
