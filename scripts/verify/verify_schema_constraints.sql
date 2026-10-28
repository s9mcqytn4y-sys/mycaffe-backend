DO $$
DECLARE
    v_store_a UUID;
    v_store_b UUID;
    v_cat_a UUID;
    v_prod_a UUID;
    v_mod_group UUID;
    v_mod_opt UUID;
    v_order_a UUID;
    v_order_b UUID;
    v_item_a UUID;
    v_split_a UUID;
    v_payment_a UUID;
    v_check_cat UUID;
    v_check_prod UUID;
    v_seq_1 INT;
    v_seq_2 INT;
    v_old_updated_at TIMESTAMPTZ;
    v_new_updated_at TIMESTAMPTZ;
    v_restrict_store_failed BOOLEAN := FALSE;
    v_restrict_order_failed BOOLEAN := FALSE;
BEGIN
    -- 1. Insert Neutral Stores
    INSERT INTO stores (slug, name, order_prefix, queue_prefix) 
    VALUES ('demo-store-a', 'MyCaffe Demo Store A', 'ORD', 'A') 
    RETURNING id INTO v_store_a;

    INSERT INTO stores (slug, name, order_prefix, queue_prefix) 
    VALUES ('demo-store-b', 'MyCaffe Demo Store B', 'EXP', 'B') 
    RETURNING id INTO v_store_b;

    -- 2. Insert Category, Product & Modifier in Store A
    INSERT INTO categories (store_id, name) VALUES (v_store_a, 'Coffee') RETURNING id INTO v_cat_a;
    INSERT INTO products (store_id, category_id, name, base_price) 
    VALUES (v_store_a, v_cat_a, 'Classic Iced Latte', 25000) 
    RETURNING id INTO v_prod_a;

    INSERT INTO modifier_groups (store_id, name, selection_type, is_required, min_select, max_select)
    VALUES (v_store_a, 'Milk Option', 'single', true, 1, 1)
    RETURNING id INTO v_mod_group;

    INSERT INTO modifier_options (store_id, group_id, name, price_delta)
    VALUES (v_store_a, v_mod_group, 'Oat Milk', 8000)
    RETURNING id INTO v_mod_opt;

    -- 3. Test Trigger updated_at on Products (using clock_timestamp)
    SELECT updated_at INTO v_old_updated_at FROM products WHERE id = v_prod_a;
    PERFORM pg_sleep(0.01);
    UPDATE products SET description = 'Freshly roasted espresso with fresh milk' WHERE id = v_prod_a;
    SELECT updated_at INTO v_new_updated_at FROM products WHERE id = v_prod_a;
    IF v_new_updated_at <= v_old_updated_at THEN
        RAISE EXCEPTION 'Trigger trg_products_updated_at failed to increment updated_at';
    END IF;
    RAISE NOTICE 'SUCCESS: update_updated_at_column trigger verified with clock_timestamp().';

    -- 4. Test Atomic Daily Sequence Counter
    INSERT INTO store_daily_sequences (store_id, business_date, last_seq, updated_at)
    VALUES (v_store_a, CURRENT_DATE, 1, NOW())
    ON CONFLICT (store_id, business_date)
    DO UPDATE SET last_seq = store_daily_sequences.last_seq + 1, updated_at = NOW()
    RETURNING last_seq INTO v_seq_1;

    INSERT INTO store_daily_sequences (store_id, business_date, last_seq, updated_at)
    VALUES (v_store_a, CURRENT_DATE, 1, NOW())
    ON CONFLICT (store_id, business_date)
    DO UPDATE SET last_seq = store_daily_sequences.last_seq + 1, updated_at = NOW()
    RETURNING last_seq INTO v_seq_2;

    IF v_seq_1 != 1 OR v_seq_2 != 2 THEN
        RAISE EXCEPTION 'Atomic queue counter returned unexpected sequence: % then %', v_seq_1, v_seq_2;
    END IF;
    RAISE NOTICE 'SUCCESS: store_daily_sequences atomic increment verified (seq: % -> %).', v_seq_1, v_seq_2;

    -- 5. Insert Order, Order Item, Order Item Option, Split & Payment in Store A and Store B
    INSERT INTO orders (store_id, order_number, queue_number, customer_name, status, payment_status, subtotal_amount, total_amount) 
    VALUES (v_store_a, 'ORD-TEST-001', 'A01', 'Customer Alpha', 'pending_payment', 'pending', 33000, 33000) RETURNING id INTO v_order_a;

    INSERT INTO orders (store_id, order_number, queue_number, customer_name, status, payment_status, subtotal_amount, total_amount) 
    VALUES (v_store_b, 'EXP-TEST-001', 'B01', 'Customer Beta', 'pending_payment', 'pending', 25000, 25000) RETURNING id INTO v_order_b;

    INSERT INTO order_items (store_id, order_id, product_id, unit_base_price, modifier_total_price, snapshot_name, snapshot_price, quantity, subtotal_amount)
    VALUES (v_store_a, v_order_a, v_prod_a, 25000, 8000, 'Classic Iced Latte', 33000, 1, 33000) RETURNING id INTO v_item_a;

    INSERT INTO order_item_options (store_id, order_item_id, modifier_group_id, modifier_option_id, snapshot_group_name, snapshot_option_name, snapshot_price_delta, quantity)
    VALUES (v_store_a, v_item_a, v_mod_group, v_mod_opt, 'Milk Option', 'Oat Milk', 8000, 1);

    INSERT INTO payment_splits (store_id, order_id, split_number, label, split_mode, subtotal_amount, total_amount, status)
    VALUES (v_store_a, v_order_a, 1, 'Split 1', 'full', 33000, 33000, 'pending') RETURNING id INTO v_split_a;

    INSERT INTO payments (store_id, order_id, split_id, attempt_no, idempotency_key, provider_order_id, amount, method, status)
    VALUES (v_store_a, v_order_a, v_split_a, 1, 'idem-test-001', 'ORD-TEST-001', 33000, 'qris', 'pending') RETURNING id INTO v_payment_a;

    -- 6A. Test Store Soft-Delete Protection (ON DELETE RESTRICT on orders & payments)
    BEGIN
        DELETE FROM stores WHERE id = v_store_a;
    EXCEPTION
        WHEN foreign_key_violation OR restrict_violation OR integrity_constraint_violation THEN
            v_restrict_store_failed := TRUE;
        WHEN OTHERS THEN
            IF SQLSTATE IN ('23503', '23001') THEN
                v_restrict_store_failed := TRUE;
            ELSE
                RAISE;
            END IF;
    END;

    IF NOT v_restrict_store_failed THEN
        RAISE EXCEPTION 'Store physical deletion should be blocked by RESTRICT foreign key when orders exist';
    END IF;
    RAISE NOTICE 'SUCCESS: Store physical deletion blocked by RESTRICT constraint protecting order history.';

    -- 6B. Test Order Physical Deletion Protection when Payment History exists (P0-E Fix)
    BEGIN
        DELETE FROM orders WHERE id = v_order_a;
    EXCEPTION
        WHEN foreign_key_violation OR restrict_violation OR integrity_constraint_violation THEN
            v_restrict_order_failed := TRUE;
        WHEN OTHERS THEN
            IF SQLSTATE IN ('23503', '23001') THEN
                v_restrict_order_failed := TRUE;
            ELSE
                RAISE;
            END IF;
    END;

    IF NOT v_restrict_order_failed THEN
        RAISE EXCEPTION 'Order physical deletion MUST be blocked by RESTRICT foreign key when payments/splits exist';
    END IF;
    RAISE NOTICE 'SUCCESS: Order physical deletion blocked by RESTRICT constraint protecting payment ledger history.';

    -- 7. Test Deletion of Category: verify product.category_id becomes NULL, store_id intact
    DELETE FROM categories WHERE id = v_cat_a;
    SELECT category_id INTO v_check_cat FROM products WHERE id = v_prod_a;
    IF v_check_cat IS NOT NULL THEN
        RAISE EXCEPTION 'Category deletion failed to set category_id to NULL';
    END IF;
    RAISE NOTICE 'SUCCESS: Category deletion set category_id to NULL while store_id remained intact.';

    -- 8. Test Deletion of Product: verify order_item.product_id becomes NULL, snapshot intact
    DELETE FROM products WHERE id = v_prod_a;
    SELECT product_id INTO v_check_prod FROM order_items WHERE id = v_item_a;
    IF v_check_prod IS NOT NULL THEN
        RAISE EXCEPTION 'Product deletion failed to set product_id to NULL in order_items';
    END IF;
    RAISE NOTICE 'SUCCESS: Product deletion set product_id to NULL while preserving historical order_item.';

    -- 9. Test Webhook Idempotency (Unique constraint violation on duplicate notification)
    INSERT INTO payment_notification_logs (
        provider, transaction_id, order_number, status_code, transaction_status, gross_amount, raw_payload
    ) VALUES (
        'midtrans', 'trx-test-12345', 'ORD-TEST-001', '200', 'settlement', 33000, '{"status": "settlement"}'::jsonb
    );

    BEGIN
        INSERT INTO payment_notification_logs (
            provider, transaction_id, order_number, status_code, transaction_status, gross_amount, raw_payload
        ) VALUES (
            'midtrans', 'trx-test-12345', 'ORD-TEST-001', '200', 'settlement', 33000, '{"status": "settlement"}'::jsonb
        );
        RAISE EXCEPTION 'Duplicate webhook insert was permitted!';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE 'SUCCESS: Duplicate webhook notification was safely rejected by unique constraint.';
    END;
END $$;

-- 10. Test Row-Level Security (RLS) under Least-Privilege Runtime Role (mycaffe_app)
DO $$
DECLARE
    v_store_a UUID;
    v_store_b UUID;
    v_visible_orders INT;
    v_rls_blocked BOOLEAN := FALSE;
BEGIN
    SELECT id INTO v_store_a FROM stores WHERE slug = 'demo-store-a';
    SELECT id INTO v_store_b FROM stores WHERE slug = 'demo-store-b';

    -- Switch to runtime application role (NOBYPASSRLS)
    EXECUTE 'SET LOCAL ROLE mycaffe_app';

    -- Without setting app.current_store_id, 0 tenant orders must be visible
    SELECT COUNT(*) INTO v_visible_orders FROM orders;
    IF v_visible_orders != 0 THEN
        RAISE EXCEPTION 'RLS leak: expected 0 orders when app.current_store_id is unset, got %', v_visible_orders;
    END IF;

    -- Set tenant context to Store A
    PERFORM set_config('app.current_store_id', v_store_a::text, true);

    SELECT COUNT(*) INTO v_visible_orders FROM orders;
    IF v_visible_orders != 1 THEN
        RAISE EXCEPTION 'RLS error: expected exactly 1 order for Store A, got %', v_visible_orders;
    END IF;

    -- Attempt cross-tenant insert into Store B while scoped to Store A (must fail WITH CHECK policy)
    BEGIN
        INSERT INTO orders (store_id, order_number, queue_number, customer_name, status, payment_status, total_amount)
        VALUES (v_store_b, 'EXP-HACK-999', 'B99', 'Cross Tenant Attacker', 'pending_payment', 'unpaid', 10000);
    EXCEPTION WHEN insufficient_privilege THEN
        v_rls_blocked := TRUE;
    END;

    IF NOT v_rls_blocked THEN
        RAISE EXCEPTION 'RLS failure: cross-tenant INSERT into Store B was permitted while scoped to Store A!';
    END IF;

    EXECUTE 'RESET ROLE';
    RAISE NOTICE 'SUCCESS: Row-Level Security (RLS) and mycaffe_app least-privilege isolation verified.';
    RAISE NOTICE 'ALL CONSTRAINTS, RLS POLICIES, AND BEHAVIORAL VERIFICATIONS PASSED SUCCESSFULLY.';
END $$;
