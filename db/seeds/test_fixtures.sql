-- db/seeds/test_fixtures.sql (CI & Integration Test Fixtures Only - APP_ENV=test)
-- Development & Demo Seed Data for Cassy Core POS (by AzureDevID)
-- Multi-tenant realistic catalog with Indonesian Rupiah whole integer amounts, Basis Points (BPS),
-- WebP studio media assets on Cloudflare R2 CDN (https://cdn.abbdullaziz.online/seeds/...),
-- and ZERO pre-seeded orders/transactions so demo environments start with a clean financial ledger.
-- STRICTLY ISOLATED TO LOCAL / CI / DEMO DEVELOPMENT ONLY. NEVER EXECUTE IN PRODUCTION!

-- =============================================================================
-- 1. STORES (TENANTS) & OPERATING POLICIES (store_settings)
-- =============================================================================
INSERT INTO stores (
    id, slug, name, address, phone, timezone, is_active,
    order_prefix, queue_prefix, default_billing_mode, shift_mode,
    enabled_payment_methods, tax_bps, service_bps, qris_mdr_bps, currency
)
VALUES
    (
        '11111111-1111-4111-8111-111111111111',
        'central-cafe',
        'Cassy Roastery Senopati',
        'Jl. Senopati No. 88, Kebayoran Baru, Jakarta Selatan 12190',
        '021-52908811',
        'Asia/Jakarta',
        true,
        'ORD',
        'A',
        'pay_first',
        'single_auto',
        '["qris","bank_transfer","gopay","shopeepay","cash","static_qris","edc"]'::jsonb,
        1000,
        500,
        70,
        'IDR'
    ),
    (
        '22222222-2222-4222-8222-222222222222',
        'express-counter',
        'Cassy Express Sudirman MRT',
        'Stasiun MRT Istora Mandiri Pintu B, Jl. Jend. Sudirman Kav. 52-53, Jakarta Selatan 12190',
        '021-57901122',
        'Asia/Jakarta',
        true,
        'EXP',
        'B',
        'pay_first',
        'single_auto',
        '["qris","gopay","shopeepay","cash","static_qris"]'::jsonb,
        1000,
        0,
        70,
        'IDR'
    )
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    address = EXCLUDED.address,
    phone = EXCLUDED.phone,
    enabled_payment_methods = EXCLUDED.enabled_payment_methods,
    updated_at = NOW();

-- =============================================================================
-- 2. USERS (STAFF - 4 Operational Roles: owner, cashier, barista, kitchen)
-- Development-only PIN for every seeded user: 482913 (argon2id, ADR-003 D2).
-- Owner email is kept strictly as abdulaziz543333@gmail.com with verified test phone +628567060477.
-- =============================================================================
INSERT INTO users (id, store_id, email, full_name, role, phone, pin_hash, is_active, pin_set_at)
VALUES
    -- Store 1: Cassy Roastery Senopati
    ('a0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'abdulaziz543333@gmail.com', 'Abdul Aziz (Owner & Principal Roaster)', 'owner', '+628567060477', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'kitchen.central@mycaffe.local', 'Chef Rendra Pratama (Head Kitchen)', 'kitchen', '+628129008812', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'cashier.central@mycaffe.local', 'Nadya Kusuma (Senior Cashier)', 'cashier', '+628129008813', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'barista.central@mycaffe.local', 'Dimas Saputra (Head Barista)', 'barista', '+628129008814', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', 'barista.roastery@abbdullaziz.online', 'Bagas Adi Nugroho (Specialty Barista)', 'barista', '+628129008816', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', 'pastry.senopati@abbdullaziz.online', 'Kezia Maharani (Pastry & Viennoiserie Chef)', 'kitchen', '+628129008817', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    -- Store 2: Cassy Express Sudirman MRT
    ('a0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'cashier.express@mycaffe.local', 'Larasati Putri (Express Cashier)', 'cashier', '+628129008821', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000008-0000-4000-8000-000000000008', '22222222-2222-4222-8222-222222222222', 'abdulaziz543333@gmail.com', 'Abdul Aziz (Owner & Principal Roaster)', 'owner', '+628567060477', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000009-0000-4000-8000-000000000009', '22222222-2222-4222-8222-222222222222', 'barista.express@abbdullaziz.online', 'Rizky Ramadhan (Express Barista)', 'barista', '+628129008823', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW())
ON CONFLICT (id) DO UPDATE SET
    email = EXCLUDED.email,
    full_name = EXCLUDED.full_name,
    phone = EXCLUDED.phone,
    pin_hash = EXCLUDED.pin_hash,
    pin_set_at = COALESCE(users.pin_set_at, NOW());

-- Store Operating Policies (store_settings)
INSERT INTO store_settings (
    store_id, void_refund_owner_threshold, cashier_max_discount_bps, cash_variance_tolerance,
    cashier_handles_bar, table_idle_close_minutes, qr_ordering_paused, static_qris_fallback_mode,
    table_time_limit_minutes, table_timer_warning_minutes, updated_by
)
VALUES
    ('11111111-1111-4111-8111-111111111111', 100000, 1000, 5000, false, 30, false, 'auto', 0, 10, 'a0000001-0000-4000-8000-000000000001'),
    ('22222222-2222-4222-8222-222222222222', 50000, 1000, 5000, true, 20, false, 'auto', 0, 10, 'a0000008-0000-4000-8000-000000000008')
ON CONFLICT (store_id) DO NOTHING;

-- =============================================================================
-- 3. CATEGORIES (7 Categories in Flagship Senopati + 4 Categories in Express MRT)
-- =============================================================================
INSERT INTO categories (id, store_id, name, display_order, is_active)
VALUES
    -- Store 1: Flagship Roastery Senopati
    ('c0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Signature Series', 1, true),
    ('c0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Espresso & Coffee', 2, true),
    ('c0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Manual Brew & Specialty', 3, true),
    ('c0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'Artisan Pastry & Bakery', 4, true),
    ('c0000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', 'Matcha, Chocolate & Non-Coffee', 5, true),
    ('c0000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', 'All-Day Brunch & Savory Kitchen', 6, true),
    ('c0000008-0000-4000-8000-000000000008', '11111111-1111-4111-8111-111111111111', 'Roastery Whole Beans & Take-Home', 7, true),
    -- Store 2: Express Counter Sudirman MRT
    ('c0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'Quick Grab Coffee', 1, true),
    ('c0000009-0000-4000-8000-000000000009', '22222222-2222-4222-8222-222222222222', 'Quick Grab Non-Coffee', 2, true),
    ('c0000010-0000-4000-8000-000000000010', '22222222-2222-4222-8222-222222222222', 'Bottled 500ml & 1L Grab-and-Go', 3, true),
    ('c0000011-0000-4000-8000-000000000011', '22222222-2222-4222-8222-222222222222', 'Express Croissant & Morning Bakery', 4, true)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    display_order = EXCLUDED.display_order,
    is_active = EXCLUDED.is_active;

-- =============================================================================
-- 4. PRODUCTS (38 Realistic Menu Items with Cloudflare R2 WebP Images)
-- Whole integer IDR (e.g. Rp 24.000 = 24000). Core IDs d0000001..d0000006 keep
-- exact pricing & stock semantics required by automated integration tests.
-- =============================================================================
INSERT INTO products (
    id, store_id, category_id, name, description, base_price, image_url,
    product_type, prep_station, display_order, is_available, track_stock, stock_quantity
)
VALUES
    -- Store 1: Signature Series (c0000001)
    ('d0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Classic Iced Latte', 'Double espresso shot with fresh dairy milk and artisanal sugar', 24000, 'https://cdn.abbdullaziz.online/seeds/products/classic-iced-latte-1x1.webp', 'prepared', 'bar', 1, true, false, 0),
    ('d0000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Es Kopi Susu Aren Senopati', 'Double ristretto house blend dengan nektar gula aren organik Jawa Barat dan susu segar dingin', 26000, 'https://cdn.abbdullaziz.online/seeds/products/kopi-susu-gula-aren-senopati-1x1.webp', 'prepared', 'bar', 2, true, false, 0),
    ('d0000008-0000-4000-8000-000000000008', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Salted Caramel Cloud Macchiato', 'Espresso single origin di atas susu dingin vanilla Madagaskar dengan lapisan sea-salt caramel cold foam', 34000, 'https://cdn.abbdullaziz.online/seeds/products/salted-caramel-cloud-macchiato-1x1.webp', 'prepared', 'bar', 3, true, false, 0),
    ('d0000009-0000-4000-8000-000000000009', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Pistachio Crema Cold Latte', 'Paduan pasta pistachio panggang Sisilia, double ristretto, dan taburan kacang pistachio renyah', 38000, 'https://cdn.abbdullaziz.online/seeds/products/pistachio-crema-cold-latte-1x1.webp', 'prepared', 'bar', 4, true, false, 0),
    ('d0000010-0000-4000-8000-000000000010', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Yuzu Espresso Tonica', 'Segarnya citrus yuzu Jepang berpadu dengan botanical tonic water dan double shot espresso Aceh Gayo', 34000, 'https://cdn.abbdullaziz.online/seeds/products/yuzu-espresso-tonica-1x1.webp', 'prepared', 'bar', 5, true, false, 0),

    -- Store 1: Espresso & Coffee (c0000002)
    ('d0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Caffe Latte', 'Espresso with steamed fresh milk and microfoam', 26000, 'https://cdn.abbdullaziz.online/seeds/products/caffe-latte-1x1.webp', 'prepared', 'bar', 1, true, false, 0),
    ('d0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Americano (Double Shot)', 'Double ristretto poured over filtered water', 20000, 'https://cdn.abbdullaziz.online/seeds/products/americano-double-shot-1x1.webp', 'prepared', 'bar', 2, true, false, 0),
    ('d0000011-0000-4000-8000-000000000011', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Velvet Cappuccino', 'Double espresso dengan microfoam tebal bertekstur beludru dan taburan bubuk kakao Valrhona', 28000, 'https://cdn.abbdullaziz.online/seeds/products/velvet-cappuccino-1x1.webp', 'prepared', 'bar', 3, true, false, 0),
    ('d0000012-0000-4000-8000-000000000012', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Flat White Double Ristretto', 'Ekstraksi 20g double ristretto pekat dengan lapisan tipis steamed milk 62 derajat Celsius', 30000, 'https://cdn.abbdullaziz.online/seeds/products/flat-white-double-ristretto-1x1.webp', 'prepared', 'bar', 4, true, false, 0),
    ('d0000013-0000-4000-8000-000000000013', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Piccolo Latte (4 oz)', 'Single ristretto shot dalam gelas demitasse 120ml dengan rasio susu hangat yang intens dan manis alami', 24000, 'https://cdn.abbdullaziz.online/seeds/products/piccolo-latte-4oz-1x1.webp', 'prepared', 'bar', 5, true, false, 0),

    -- Store 1: Manual Brew & Specialty (c0000003)
    ('d0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'V60 Single Origin Gayo', 'Filter brewed single origin with floral citrus notes', 32000, 'https://cdn.abbdullaziz.online/seeds/products/v60-single-origin-gayo-1x1.webp', 'prepared', 'bar', 1, true, true, 45),
    ('d0000014-0000-4000-8000-000000000014', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'Japanese Iced Filter Bali Kintamani', 'Seduhan pour-over langsung di atas kristal es dengan karakter jeruk manis dan gula merah', 34000, 'https://cdn.abbdullaziz.online/seeds/products/japanese-iced-filter-kintamani-1x1.webp', 'prepared', 'bar', 2, true, true, 40),
    ('d0000015-0000-4000-8000-000000000015', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'Kalita Wave Flores Bajawa', 'Seduhan flat-bottom dripper menghasilkan body medium-full dengan aroma cokelat hitam dan rempah manis', 35000, 'https://cdn.abbdullaziz.online/seeds/products/kalita-wave-flores-bajawa-1x1.webp', 'prepared', 'bar', 3, true, true, 35),
    ('d0000016-0000-4000-8000-000000000016', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'Nitro Cold Brew Cascade', 'Cold brew 18 jam yang diinfusi gas nitrogen murni langsung dari tap dengan efek kaskade lembut', 36000, 'https://cdn.abbdullaziz.online/seeds/products/nitro-cold-brew-cascade-1x1.webp', 'prepared', 'bar', 4, true, true, 30),

    -- Store 1: Artisan Pastry & Bakery (c0000004)
    ('d0000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Butter Croissant Artisanal', 'Freshly baked flaky French butter croissant', 24000, 'https://cdn.abbdullaziz.online/seeds/products/butter-croissant-artisanal-1x1.webp', 'ready_to_serve', 'kitchen', 1, true, true, 18),
    ('d0000017-0000-4000-8000-000000000017', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Pain au Chocolat Valrhona', 'Pastri viennoiserie berlapis renyah dengan dua batang cokelat hitam Valrhona 66% di bagian tengah', 28000, 'https://cdn.abbdullaziz.online/seeds/products/pain-au-chocolat-valrhona-1x1.webp', 'ready_to_serve', 'kitchen', 2, true, true, 24),
    ('d0000018-0000-4000-8000-000000000018', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Caramelized Almond Kouign-Amann', 'Pastri klasik Brittany dengan kerak gula karamel mentega asin, krim frangipane, dan irisan almond panggang', 32000, 'https://cdn.abbdullaziz.online/seeds/products/almond-kouign-amann-1x1.webp', 'ready_to_serve', 'kitchen', 3, true, true, 20),
    ('d0000019-0000-4000-8000-000000000019', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Basque Burnt Cheesecake Slice', 'Cheesecake panggang suhu tinggi bergaya San Sebastian dengan bagian tengah lumer dan permukaan karamel', 42000, 'https://cdn.abbdullaziz.online/seeds/products/basque-burnt-cheesecake-1x1.webp', 'ready_to_serve', 'kitchen', 4, true, true, 16),

    -- Store 1: Matcha, Chocolate & Non-Coffee (c0000006)
    ('d0000020-0000-4000-8000-000000000020', '11111111-1111-4111-8111-111111111111', 'c0000006-0000-4000-8000-000000000006', 'Uji Ceremonial Matcha Latte', 'Matcha panen pertama dari Uji, Kyoto yang dikocok dengan chasen bambu di atas susu segar pilihan', 36000, 'https://cdn.abbdullaziz.online/seeds/products/uji-ceremonial-matcha-latte-1x1.webp', 'prepared', 'bar', 1, true, false, 0),
    ('d0000021-0000-4000-8000-000000000021', '11111111-1111-4111-8111-111111111111', 'c0000006-0000-4000-8000-000000000006', 'Hojicha Roasted Oat Latte', 'Teh hijau panggang arang khas Jepang dengan aroma smoky karamel dan oat milk barista blend', 35000, 'https://cdn.abbdullaziz.online/seeds/products/hojicha-roasted-oat-latte-1x1.webp', 'prepared', 'bar', 2, true, false, 0),
    ('d0000022-0000-4000-8000-000000000022', '11111111-1111-4111-8111-111111111111', 'c0000006-0000-4000-8000-000000000006', '72% Single Origin Choco Karo', 'Ganache cokelat hitam single-origin Tanah Karo Sumatera dengan tekstur kental dan rasa buah beri gelap', 34000, 'https://cdn.abbdullaziz.online/seeds/products/single-origin-dark-choco-karo-1x1.webp', 'prepared', 'bar', 3, true, false, 0),
    ('d0000023-0000-4000-8000-000000000023', '11111111-1111-4111-8111-111111111111', 'c0000006-0000-4000-8000-000000000006', 'Hibiscus Berry Cold Brew Tea', 'Seduhan dingin kelopak bunga rosella merah, buah beri hutan, dan daun mint segar bebas kafein', 28000, 'https://cdn.abbdullaziz.online/seeds/products/hibiscus-berry-artisan-tea-1x1.webp', 'prepared', 'bar', 4, true, false, 0),

    -- Store 1: All-Day Brunch & Savory Kitchen (c0000007)
    ('d0000024-0000-4000-8000-000000000024', '11111111-1111-4111-8111-111111111111', 'c0000007-0000-4000-8000-000000000007', 'Truffle Egg & Smoked Beef Brioche', 'Roti brioche panggang mentega dengan telur kampung soft-scramble, smoked beef brisket, dan black truffle aioli', 58000, 'https://cdn.abbdullaziz.online/seeds/products/truffle-scrambled-egg-brioche-1x1.webp', 'prepared', 'kitchen', 1, true, true, 25),
    ('d0000025-0000-4000-8000-000000000025', '11111111-1111-4111-8111-111111111111', 'c0000007-0000-4000-8000-000000000007', 'Crispy Chicken Sambal Matah Bowl', 'Nasi hangat pandan wangi dengan paha ayam goreng renyah, sambal matah kecombrang Bali, dan telur onsen', 52000, 'https://cdn.abbdullaziz.online/seeds/products/crispy-chicken-sambal-matah-bowl-1x1.webp', 'prepared', 'kitchen', 2, true, true, 30),
    ('d0000026-0000-4000-8000-000000000026', '11111111-1111-4111-8111-111111111111', 'c0000007-0000-4000-8000-000000000007', 'Wagyu Beef Lasagna Al Forno', 'Lapisan pasta segar dengan ragu daging sapi Wagyu masak lambat 6 jam, saus bechamel, dan keju Parmigiano', 68000, 'https://cdn.abbdullaziz.online/seeds/products/wagyu-beef-lasagna-al-forno-1x1.webp', 'prepared', 'kitchen', 3, true, true, 15),
    ('d0000027-0000-4000-8000-000000000027', '11111111-1111-4111-8111-111111111111', 'c0000007-0000-4000-8000-000000000007', 'Truffle Parmesan Pommes Frites', 'Kentang goreng shoestring renyah dengan minyak truffle putih, parutan keju Grana Padano, dan rosemary', 42000, 'https://cdn.abbdullaziz.online/seeds/products/truffle-parmesan-pommes-frites-1x1.webp', 'prepared', 'kitchen', 4, true, false, 0),

    -- Store 1: Roastery Whole Beans & Take-Home (c0000008)
    ('d0000028-0000-4000-8000-000000000028', '11111111-1111-4111-8111-111111111111', 'c0000008-0000-4000-8000-000000000008', 'Espresso Crema House Blend (Beans)', 'Biji kopi sangrai medium-dark (70% Arabika Aceh Gayo, 30% Fine Robusta Temanggung) dengan katup degassing', 95000, 'https://cdn.abbdullaziz.online/seeds/products/roastery-house-blend-beans-1x1.webp', 'ready_to_serve', 'none', 1, true, true, 50),
    ('d0000029-0000-4000-8000-000000000029', '11111111-1111-4111-8111-111111111111', 'c0000008-0000-4000-8000-000000000008', 'Aceh Gayo Natural Wine Process', 'Biji kopi single origin Takengon 1500 mdpl proses fermentasi natural dengan aroma buah anggur dan kakao', 125000, 'https://cdn.abbdullaziz.online/seeds/products/aceh-gayo-wine-process-beans-1x1.webp', 'ready_to_serve', 'none', 2, true, true, 30),

    -- Store 2: Quick Grab Coffee (c0000005)
    ('d0000006-0000-4000-8000-000000000006', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Express Cold Brew', 'Steeped for 18 hours, rich and smooth finish', 22000, 'https://cdn.abbdullaziz.online/seeds/products/express-cold-brew-1x1.webp', 'prepared', 'bar', 1, true, false, 0),
    ('d0000030-0000-4000-8000-000000000030', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Es Kopi Susu Komuter MRT', 'Kopi susu gula aren khas komuter Sudirman disajikan cepat dalam cup bersegel anti-tumpah', 20000, 'https://cdn.abbdullaziz.online/seeds/products/es-kopi-susu-komuter-1x1.webp', 'prepared', 'bar', 2, true, false, 0),
    ('d0000031-0000-4000-8000-000000000031', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Shaken Brown Sugar Americano', 'Double espresso yang dikocok cepat dengan es kristal dan sirup molase gula merah menghasilkan buih lembut', 22000, 'https://cdn.abbdullaziz.online/seeds/products/iced-shaken-brown-sugar-americano-1x1.webp', 'prepared', 'bar', 3, true, false, 0),

    -- Store 2: Quick Grab Non-Coffee (c0000009)
    ('d0000032-0000-4000-8000-000000000032', '22222222-2222-4222-8222-222222222222', 'c0000009-0000-4000-8000-000000000009', 'Express Iced Matcha Boost', 'Minuman matcha Kyoto dingin dengan susu segar untuk energi pagi bebas kopi', 26000, 'https://cdn.abbdullaziz.online/seeds/products/express-iced-matcha-boost-1x1.webp', 'prepared', 'bar', 1, true, false, 0),
    ('d0000033-0000-4000-8000-000000000033', '22222222-2222-4222-8222-222222222222', 'c0000009-0000-4000-8000-000000000009', 'Iced Belgian Choco Malt', 'Es cokelat Belgia pekat dengan taburan malt renyah di atas milk foam', 25000, 'https://cdn.abbdullaziz.online/seeds/products/iced-belgian-choco-malt-1x1.webp', 'prepared', 'bar', 2, true, false, 0),

    -- Store 2: Bottled 500ml & 1L Grab-and-Go (c0000010)
    ('d0000034-0000-4000-8000-000000000034', '22222222-2222-4222-8222-222222222222', 'c0000010-0000-4000-8000-000000000010', 'Botol Kopi Susu Aren (500ml)', 'Kemasan botol kaca 500ml (3-4 porsi saji) siap simpan di kulkas kantor atau rumah', 55000, 'https://cdn.abbdullaziz.online/seeds/products/botol-kopi-susu-aren-500ml-1x1.webp', 'ready_to_serve', 'bar', 1, true, true, 25),
    ('d0000035-0000-4000-8000-000000000035', '22222222-2222-4222-8222-222222222222', 'c0000010-0000-4000-8000-000000000010', 'Botol Cold Brew Black (500ml)', 'Konsentrat cold brew single origin bebas gula dalam botol segel 500ml', 52000, 'https://cdn.abbdullaziz.online/seeds/products/botol-cold-brew-black-500ml-1x1.webp', 'ready_to_serve', 'bar', 2, true, true, 20),

    -- Store 2: Express Croissant & Morning Bakery (c0000011)
    ('d0000036-0000-4000-8000-000000000036', '22222222-2222-4222-8222-222222222222', 'c0000011-0000-4000-8000-000000000011', 'Smoked Beef & Cheddar Croissant', 'Butter croissant hangat berisi irisan daging sapi asap dan keju red cheddar leleh', 34000, 'https://cdn.abbdullaziz.online/seeds/products/smoked-beef-cheddar-croissant-1x1.webp', 'ready_to_serve', 'bar', 1, true, true, 20),
    ('d0000037-0000-4000-8000-000000000037', '22222222-2222-4222-8222-222222222222', 'c0000011-0000-4000-8000-000000000011', 'Cinnamon Sugar Brioche Cruffin', 'Kombinasi croissant dan muffin berlapis gula kayu manis dengan isian krim vanila', 26000, 'https://cdn.abbdullaziz.online/seeds/products/cinnamon-sugar-cruffin-1x1.webp', 'ready_to_serve', 'bar', 2, true, true, 18),
    ('d0000038-0000-4000-8000-000000000038', '22222222-2222-4222-8222-222222222222', 'c0000011-0000-4000-8000-000000000011', 'Banana Walnut Espresso Loaf', 'Bolu pisang raja panggang lembap dengan campuran kenari California dan sentuhan espresso', 24000, 'https://cdn.abbdullaziz.online/seeds/products/banana-walnut-coffee-cake-1x1.webp', 'ready_to_serve', 'bar', 3, true, true, 22)
ON CONFLICT (id) DO UPDATE SET
    category_id = EXCLUDED.category_id,
    description = EXCLUDED.description,
    image_url = EXCLUDED.image_url,
    product_type = EXCLUDED.product_type,
    prep_station = EXCLUDED.prep_station,
    display_order = EXCLUDED.display_order;

-- =============================================================================
-- 5. PRODUCT VARIANTS (migration 000008, owner decision K1)
-- Every product has at least one default 'Reguler' variant; selected beverages,
-- bottled drinks, and roastery bean packs expose additional size/pack variants.
-- Note: Caffe Latte (d0000002) keeps exactly 2 variants (Reguler + Large) for
-- compatibility with TestIntegrationCatalog.
-- =============================================================================
INSERT INTO product_variants (store_id, product_id, name, sku, price, is_default, is_available, display_order)
SELECT p.store_id, p.id, 'Reguler', '', p.base_price, true, true, 0
FROM products p
WHERE NOT EXISTS (
    SELECT 1 FROM product_variants v WHERE v.store_id = p.store_id AND v.product_id = p.id
);

-- Assign clean default SKUs to seeded default variants where empty
UPDATE product_variants SET sku = 'LAT-R'
WHERE product_id = 'd0000002-0000-4000-8000-000000000002' AND is_default AND sku = '';

-- Additional multi-size & multi-pack variants
INSERT INTO product_variants (id, store_id, product_id, name, sku, price, is_default, is_available, display_order)
VALUES
    -- Caffe Latte Large (exact ID preserved for tests)
    ('e0000002-0000-4000-8000-000000000022', '11111111-1111-4111-8111-111111111111', 'd0000002-0000-4000-8000-000000000002', 'Large', 'LAT-L', 32000, false, true, 1),
    -- Signature Series Large (16 oz) options
    ('e0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'd0000007-0000-4000-8000-000000000007', 'Large (16 oz)', 'KSA-L', 32000, false, true, 1),
    ('e0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'd0000008-0000-4000-8000-000000000008', 'Large (16 oz)', 'SCM-L', 40000, false, true, 1),
    ('e0000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'd0000009-0000-4000-8000-000000000009', 'Large (16 oz)', 'PCL-L', 44000, false, true, 1),
    -- Espresso & Matcha Large / Iced variants
    ('e0000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', 'd0000011-0000-4000-8000-000000000011', 'Large (16 oz)', 'CAP-L', 34000, false, true, 1),
    ('e0000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', 'd0000020-0000-4000-8000-000000000020', 'Large (16 oz)', 'UJM-L', 42000, false, true, 1),
    ('e0000008-0000-4000-8000-000000000008', '11111111-1111-4111-8111-111111111111', 'd0000021-0000-4000-8000-000000000021', 'Disajikan Dingin (Iced)', 'HOJ-ICE', 37000, false, true, 1),
    ('e0000009-0000-4000-8000-000000000009', '11111111-1111-4111-8111-111111111111', 'd0000022-0000-4000-8000-000000000022', 'Disajikan Dingin (Iced)', 'CHK-ICE', 36000, false, true, 1),
    -- Whole Beans 500g Value Pack variants
    ('e0000010-0000-4000-8000-000000000010', '11111111-1111-4111-8111-111111111111', 'd0000028-0000-4000-8000-000000000028', 'Kemasan 500g Value Pack', 'BEAN-HB-500', 175000, false, true, 1),
    ('e0000011-0000-4000-8000-000000000011', '11111111-1111-4111-8111-111111111111', 'd0000029-0000-4000-8000-000000000029', 'Kemasan 500g Value Pack', 'BEAN-GW-500', 235000, false, true, 1),
    -- Store 2 Express: Upsize & 1-Liter Bottle variants
    ('e0000012-0000-4000-8000-000000000012', '22222222-2222-4222-8222-222222222222', 'd0000030-0000-4000-8000-000000000030', 'Large Commuter (16 oz)', 'EXP-KSK-L', 25000, false, true, 1),
    ('e0000013-0000-4000-8000-000000000013', '22222222-2222-4222-8222-222222222222', 'd0000034-0000-4000-8000-000000000034', 'Botol Family 1 Liter', 'EXP-BOT-1L', 98000, false, true, 1),
    ('e0000014-0000-4000-8000-000000000014', '22222222-2222-4222-8222-222222222222', 'd0000035-0000-4000-8000-000000000035', 'Botol Family 1 Liter', 'EXP-CBB-1L', 92000, false, true, 1)
ON CONFLICT (id) DO NOTHING;

-- =============================================================================
-- 6. MODIFIER GROUPS, OPTIONS & 1-LEVEL NESTED SUB-ADDONS
-- Core IDs 10000001..10000003 and 20000001..20000006 keep exact pricing &
-- required bounds for automated test compatibility.
-- =============================================================================
INSERT INTO modifier_groups (id, store_id, name, selection_type, is_required, min_select, max_select, display_order, is_active)
VALUES
    -- Store 1 Core & Extended Modifier Groups
    ('10000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Milk Selection', 'single', true, 1, 1, 1, true),
    ('10000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Sweetness Level', 'single', true, 1, 1, 2, true),
    ('10000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Plant Milk Preparation (Sub-Addon)', 'single', true, 1, 1, 3, true),
    ('10000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'Ice Cube Level', 'single', false, 0, 1, 4, true),
    ('10000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'Espresso & Bean Upgrade', 'multiple', false, 0, 2, 5, true),
    ('10000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', 'Bakery Serving & Spread', 'multiple', false, 0, 2, 6, true),
    ('10000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', 'Kitchen Add-Ons & Spice Preference', 'multiple', false, 0, 3, 7, true),
    ('10000008-0000-4000-8000-000000000008', '11111111-1111-4111-8111-111111111111', 'Roastery Grind Size', 'single', true, 1, 1, 8, true),
    -- Store 2 Express Modifier Groups
    ('10000009-0000-4000-8000-000000000009', '22222222-2222-4222-8222-222222222222', 'Express Sweetness & Ice', 'single', false, 0, 1, 1, true),
    ('10000010-0000-4000-8000-000000000010', '22222222-2222-4222-8222-222222222222', 'Express Milk & Foam Upgrade', 'multiple', false, 0, 2, 2, true)
ON CONFLICT (id) DO NOTHING;

INSERT INTO modifier_options (id, store_id, group_id, name, price_delta, is_available, display_order)
VALUES
    -- Group 10000001: Milk Selection
    ('20000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '10000001-0000-4000-8000-000000000001', 'Fresh Dairy Milk', 0, true, 1),
    ('20000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', '10000001-0000-4000-8000-000000000001', 'Oat Milk Barista Blend', 8000, true, 2),
    ('20000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', '10000001-0000-4000-8000-000000000001', 'Roasted Almond Breeze Barista', 8000, true, 3),
    ('20000008-0000-4000-8000-000000000008', '11111111-1111-4111-8111-111111111111', '10000001-0000-4000-8000-000000000001', 'Organic Unsweetened Soy Milk', 6000, true, 4),

    -- Group 10000002: Sweetness Level
    ('20000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', '10000002-0000-4000-8000-000000000002', 'Normal Sugar (100%)', 0, true, 1),
    ('20000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', '10000002-0000-4000-8000-000000000002', 'Less Sugar (50%)', 0, true, 2),
    ('20000009-0000-4000-8000-000000000009', '11111111-1111-4111-8111-111111111111', '10000002-0000-4000-8000-000000000002', 'No Added Sugar (0%)', 0, true, 3),

    -- Group 10000003: Plant Milk Preparation (Sub-Addon)
    ('20000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', '10000003-0000-4000-8000-000000000003', 'Standard Pour', 0, true, 1),
    ('20000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', '10000003-0000-4000-8000-000000000003', 'Whipped Oat Cold Foam', 4000, true, 2),
    ('20000010-0000-4000-8000-000000000010', '11111111-1111-4111-8111-111111111111', '10000003-0000-4000-8000-000000000003', 'Salted Caramel Oat Drizzle', 5000, true, 3),

    -- Group 10000004: Ice Cube Level
    ('20000011-0000-4000-8000-000000000011', '11111111-1111-4111-8111-111111111111', '10000004-0000-4000-8000-000000000004', 'Normal Crystal Ice', 0, true, 1),
    ('20000012-0000-4000-8000-000000000012', '11111111-1111-4111-8111-111111111111', '10000004-0000-4000-8000-000000000004', 'Less Ice (70%)', 0, true, 2),

    -- Group 10000005: Espresso & Bean Upgrade
    ('20000013-0000-4000-8000-000000000013', '11111111-1111-4111-8111-111111111111', '10000005-0000-4000-8000-000000000005', 'Extra Single Ristretto Shot', 6000, true, 1),
    ('20000014-0000-4000-8000-000000000014', '11111111-1111-4111-8111-111111111111', '10000005-0000-4000-8000-000000000005', 'Upgrade Single Origin Aceh Gayo Bean', 8000, true, 2),
    ('20000015-0000-4000-8000-000000000015', '11111111-1111-4111-8111-111111111111', '10000005-0000-4000-8000-000000000005', 'Swiss Water Decaf Espresso', 5000, true, 3),

    -- Group 10000006: Bakery Serving & Spread
    ('20000016-0000-4000-8000-000000000016', '11111111-1111-4111-8111-111111111111', '10000006-0000-4000-8000-000000000006', 'Hangatkan di Oven (Warmed)', 0, true, 1),
    ('20000017-0000-4000-8000-000000000017', '11111111-1111-4111-8111-111111111111', '10000006-0000-4000-8000-000000000006', 'Ekstra French Butter & Strawberry Jam', 6000, true, 2),

    -- Group 10000007: Kitchen Add-Ons & Spice Preference
    ('20000018-0000-4000-8000-000000000018', '11111111-1111-4111-8111-111111111111', '10000007-0000-4000-8000-000000000007', 'Sambal / Saus Dipisah (On The Side)', 0, true, 1),
    ('20000019-0000-4000-8000-000000000019', '11111111-1111-4111-8111-111111111111', '10000007-0000-4000-8000-000000000007', 'Add Free-Range Onsen Egg', 7000, true, 2),
    ('20000020-0000-4000-8000-000000000020', '11111111-1111-4111-8111-111111111111', '10000007-0000-4000-8000-000000000007', 'Extra Black Truffle Aioli Dip', 8000, true, 3),

    -- Group 10000008: Roastery Grind Size
    ('20000021-0000-4000-8000-000000000021', '11111111-1111-4111-8111-111111111111', '10000008-0000-4000-8000-000000000008', 'Whole Beans (Biji Utuh)', 0, true, 1),
    ('20000022-0000-4000-8000-000000000022', '11111111-1111-4111-8111-111111111111', '10000008-0000-4000-8000-000000000008', 'Giling Halus (Mesin Espresso)', 0, true, 2),
    ('20000023-0000-4000-8000-000000000023', '11111111-1111-4111-8111-111111111111', '10000008-0000-4000-8000-000000000008', 'Giling Medium (V60 / Pour Over)', 0, true, 3),
    ('20000024-0000-4000-8000-000000000024', '11111111-1111-4111-8111-111111111111', '10000008-0000-4000-8000-000000000008', 'Giling Kasar (French Press / Cold Brew)', 0, true, 4),

    -- Store 2 Express Modifier Options (Groups 10000009 & 10000010)
    ('20000025-0000-4000-8000-000000000025', '22222222-2222-4222-8222-222222222222', '10000009-0000-4000-8000-000000000009', 'Normal Sweet & Ice', 0, true, 1),
    ('20000026-0000-4000-8000-000000000026', '22222222-2222-4222-8222-222222222222', '10000009-0000-4000-8000-000000000009', 'Less Sugar (50%)', 0, true, 2),
    ('20000027-0000-4000-8000-000000000027', '22222222-2222-4222-8222-222222222222', '10000010-0000-4000-8000-000000000010', 'Upgrade Oat Milk Express', 7000, true, 1),
    ('20000028-0000-4000-8000-000000000028', '22222222-2222-4222-8222-222222222222', '10000010-0000-4000-8000-000000000010', 'Add Aren Cream Cold Foam', 4000, true, 2)
ON CONFLICT (id) DO NOTHING;

-- Product -> Modifier Group links
-- Note: d0000001 keeps its exact required modifier groups (10000001, 10000002);
-- d0000002, d0000003, d0000005 keep optional or no required modifiers so existing tests pass.
INSERT INTO product_modifier_groups (store_id, product_id, group_id, display_order)
VALUES
    -- d0000001: Classic Iced Latte (Required Milk + Sweetness + Optional Ice + Optional Shot)
    ('11111111-1111-4111-8111-111111111111', 'd0000001-0000-4000-8000-000000000001', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000001-0000-4000-8000-000000000001', '10000002-0000-4000-8000-000000000002', 2),
    ('11111111-1111-4111-8111-111111111111', 'd0000001-0000-4000-8000-000000000001', '10000004-0000-4000-8000-000000000004', 3),
    ('11111111-1111-4111-8111-111111111111', 'd0000001-0000-4000-8000-000000000001', '10000005-0000-4000-8000-000000000005', 4),
    -- Signature Milk Drinks (d0000007, d0000008, d0000009, d0000020, d0000021)
    ('11111111-1111-4111-8111-111111111111', 'd0000007-0000-4000-8000-000000000007', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000007-0000-4000-8000-000000000007', '10000002-0000-4000-8000-000000000002', 2),
    ('11111111-1111-4111-8111-111111111111', 'd0000008-0000-4000-8000-000000000008', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000008-0000-4000-8000-000000000008', '10000002-0000-4000-8000-000000000002', 2),
    ('11111111-1111-4111-8111-111111111111', 'd0000009-0000-4000-8000-000000000009', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000009-0000-4000-8000-000000000009', '10000002-0000-4000-8000-000000000002', 2),
    ('11111111-1111-4111-8111-111111111111', 'd0000011-0000-4000-8000-000000000011', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000012-0000-4000-8000-000000000012', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000020-0000-4000-8000-000000000020', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000020-0000-4000-8000-000000000020', '10000002-0000-4000-8000-000000000002', 2),
    -- Bakery Optional Warming & Spread (d0000005, d0000017, d0000018)
    ('11111111-1111-4111-8111-111111111111', 'd0000005-0000-4000-8000-000000000005', '10000006-0000-4000-8000-000000000006', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000017-0000-4000-8000-000000000017', '10000006-0000-4000-8000-000000000006', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000018-0000-4000-8000-000000000018', '10000006-0000-4000-8000-000000000006', 1),
    -- Kitchen Savory Add-Ons (d0000024, d0000025, d0000026)
    ('11111111-1111-4111-8111-111111111111', 'd0000024-0000-4000-8000-000000000024', '10000007-0000-4000-8000-000000000007', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000025-0000-4000-8000-000000000025', '10000007-0000-4000-8000-000000000007', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000026-0000-4000-8000-000000000026', '10000007-0000-4000-8000-000000000007', 1),
    -- Roastery Whole Beans Grind Selection (d0000028, d0000029)
    ('11111111-1111-4111-8111-111111111111', 'd0000028-0000-4000-8000-000000000028', '10000008-0000-4000-8000-000000000008', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000029-0000-4000-8000-000000000029', '10000008-0000-4000-8000-000000000008', 1),
    -- Store 2 Express Optional Modifiers (d0000030, d0000031, d0000032)
    ('22222222-2222-4222-8222-222222222222', 'd0000030-0000-4000-8000-000000000030', '10000009-0000-4000-8000-000000000009', 1),
    ('22222222-2222-4222-8222-222222222222', 'd0000030-0000-4000-8000-000000000030', '10000010-0000-4000-8000-000000000010', 2),
    ('22222222-2222-4222-8222-222222222222', 'd0000031-0000-4000-8000-000000000031', '10000009-0000-4000-8000-000000000009', 1),
    ('22222222-2222-4222-8222-222222222222', 'd0000032-0000-4000-8000-000000000032', '10000009-0000-4000-8000-000000000009', 1),
    ('22222222-2222-4222-8222-222222222222', 'd0000032-0000-4000-8000-000000000032', '10000010-0000-4000-8000-000000000010', 2)
ON CONFLICT (store_id, product_id, group_id) DO NOTHING;

-- Choosing Oat Milk (20000002) or Almond Milk (20000007) unlocks Plant Milk Preparation sub-addon group (10000003)
INSERT INTO option_modifier_groups (store_id, parent_option_id, child_group_id, display_order)
VALUES
    ('11111111-1111-4111-8111-111111111111', '20000002-0000-4000-8000-000000000002', '10000003-0000-4000-8000-000000000003', 1),
    ('11111111-1111-4111-8111-111111111111', '20000007-0000-4000-8000-000000000007', '10000003-0000-4000-8000-000000000003', 1)
ON CONFLICT (store_id, parent_option_id, child_group_id) DO NOTHING;

-- =============================================================================
-- 7. CAFE FLOOR TABLES (migration 000016) & CASH DRAWERS (migration 000010)
-- =============================================================================
INSERT INTO tables (id, store_id, table_number, label, section, capacity, qr_token, is_active)
VALUES
    -- Store 1: Flagship Roastery Senopati (8 Tables across 3 sections)
    ('50000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'M-01', 'Meja Bar Pour-Over 1', 'Indoor Roastery', 2, 'qr_senopati_m01_7f8a9b2c3d4e5f6a7b8c9d01', true),
    ('50000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'M-02', 'Meja Bar Pour-Over 2', 'Indoor Roastery', 2, 'qr_senopati_m02_7f8a9b2c3d4e5f6a7b8c9d02', true),
    ('50000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'M-03', 'Booth Sofa Jendela Utara', 'Indoor Roastery', 4, 'qr_senopati_m03_7f8a9b2c3d4e5f6a7b8c9d03', true),
    ('50000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'M-04', 'Booth Sofa Jendela Selatan', 'Indoor Roastery', 4, 'qr_senopati_m04_7f8a9b2c3d4e5f6a7b8c9d04', true),
    ('50000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'M-05', 'Communal Oak Table', 'Indoor Roastery', 8, 'qr_senopati_m05_7f8a9b2c3d4e5f6a7b8c9d05', true),
    ('50000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', 'G-01', 'Teras Taman Tropis 1', 'Outdoor Garden', 4, 'qr_senopati_g01_7f8a9b2c3d4e5f6a7b8c9d06', true),
    ('50000007-0000-4000-8000-000000000007', '11111111-1111-4111-8111-111111111111', 'G-02', 'Teras Taman Tropis 2', 'Outdoor Garden', 4, 'qr_senopati_g02_7f8a9b2c3d4e5f6a7b8c9d07', true),
    ('50000008-0000-4000-8000-000000000008', '11111111-1111-4111-8111-111111111111', 'VIP-01', 'Private Roastery Boardroom Lt. 2', 'VIP Lounge', 10, 'qr_senopati_vip1_7f8a9b2c3d4e5f6a7b8c9d08', true),
    -- Store 2: Express Counter Sudirman MRT (3 High-Stool / Pickup Zones)
    ('50000009-0000-4000-8000-000000000009', '22222222-2222-4222-8222-222222222222', 'EXP-01', 'High Stool Concourse 1', 'MRT Concourse', 2, 'qr_sudirman_exp1_7f8a9b2c3d4e5f6a7b8c9d09', true),
    ('50000010-0000-4000-8000-000000000010', '22222222-2222-4222-8222-222222222222', 'EXP-02', 'High Stool Concourse 2', 'MRT Concourse', 2, 'qr_sudirman_exp2_7f8a9b2c3d4e5f6a7b8c9d10', true),
    ('50000011-0000-4000-8000-000000000011', '22222222-2222-4222-8222-222222222222', 'EXP-03', 'Standing Quick Bar', 'MRT Concourse', 4, 'qr_sudirman_exp3_7f8a9b2c3d4e5f6a7b8c9d11', true)
ON CONFLICT (id) DO NOTHING;

INSERT INTO cash_drawers (id, store_id, name, is_active)
VALUES
    ('60000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Kasir Utama Espresso Bar Lt. 1', true),
    ('60000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Kasir Lounge & Roastery Lt. 2', true),
    ('60000003-0000-4000-8000-000000000003', '22222222-2222-4222-8222-222222222222', 'Kasir Express MRT Istora', true)
ON CONFLICT (id) DO NOTHING;

-- =============================================================================
-- 8. COMPLETED MEDIA UPLOADS LEDGER (migration 000022)
-- Records the verified .webp variants (1:1 square, 4:3 card, 1:1 thumb) on R2 CDN
-- for every seeded product.
-- =============================================================================
INSERT INTO media_uploads (
    id, store_id, kind, product_id, status, object_key, content_type, size_bytes,
    variants, created_by, created_at, expires_at, completed_at
)
SELECT
    ('700000' || right(p.id::text, 2) || '-0000-4000-8000-0000000000' || right(p.id::text, 2))::uuid,
    p.store_id,
    'product_image',
    p.id,
    'completed',
    replace(p.image_url, 'https://cdn.abbdullaziz.online/', ''),
    'image/webp',
    28400,
    jsonb_build_object(
        'original', p.image_url,
        'square_1x1', p.image_url,
        'card_4x3', replace(p.image_url, '-1x1.webp', '-4x3.webp'),
        'thumb', replace(p.image_url, '-1x1.webp', '-thumb.webp')
    ),
    CASE
        WHEN p.store_id = '11111111-1111-4111-8111-111111111111'::uuid THEN 'a0000001-0000-4000-8000-000000000001'::uuid
        ELSE 'a0000008-0000-4000-8000-000000000008'::uuid
    END,
    NOW() - INTERVAL '10 minutes',
    NOW() + INTERVAL '5 minutes',
    NOW() - INTERVAL '9 minutes'
FROM products p
WHERE p.image_url IS NOT NULL
ON CONFLICT (id) DO NOTHING;
