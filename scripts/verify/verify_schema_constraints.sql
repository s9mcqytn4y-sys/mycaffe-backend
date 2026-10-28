DO $$
DECLARE
    v_store_a UUID;
    v_store_b UUID;
    v_cat_a UUID;
    v_prod_a UUID;
    v_order_a UUID;
    v_item_a UUID;
    v_payment_a UUID;
    v_check_cat UUID;
    v_check_prod UUID;
    v_seq_1 INT;
    v_seq_2 INT;
    v_old_updated_at TIMESTAMPTZ;
    v_new_updated_at TIMESTAMPTZ;
    v_restrict_failed BOOLEAN := FALSE;
BEGIN
    -- 1. Insert Neutral Stores
    INSERT INTO stores (slug, name, order_prefix, queue_prefix) 
    VALUES ('demo-store-a', 'MyCaffe Demo Store A', 'ORD', 'A') 
    RETURNING id INTO v_store_a;

    INSERT INTO stores (slug, name, order_prefix, queue_prefix) 
    VALUES ('demo-store-b', 'MyCaffe Demo Store B', 'EXP', 'B') 
    RETURNING id INTO v_store_b;

    -- 2. Insert Category & Product in Store A
    INSERT INTO categories (store_id, name) VALUES (v_store_a, 'Coffee') RETURNING id INTO v_cat_a;
    INSERT INTO products (store_id, category_id, name, base_price) 
    VALUES (v_store_a, v_cat_a, 'Classic Iced Latte', 25000) 
    RETURNING id INTO v_prod_a;

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

    -- 5. Insert Order & Order Item in Store A with status 'pending_payment'
    INSERT INTO orders (store_id, order_number, queue_number, customer_name, status, total_amount) 
    VALUES (v_store_a, 'ORD-TEST-001', 'A01', 'Customer Alpha', 'pending_payment', 25000) RETURNING id INTO v_order_a;

    INSERT INTO order_items (store_id, order_id, product_id, snapshot_name, snapshot_price, quantity, subtotal_amount)
    VALUES (v_store_a, v_order_a, v_prod_a, 'Classic Iced Latte', 25000, 1, 25000) RETURNING id INTO v_item_a;

    INSERT INTO payments (store_id, order_id, provider_order_id, amount, method, status)
    VALUES (v_store_a, v_order_a, 'ORD-TEST-001', 25000, 'qris', 'pending') RETURNING id INTO v_payment_a;

    -- 6. Test Store Soft-Delete Protection (ON DELETE RESTRICT on orders & payments)
    BEGIN
        DELETE FROM stores WHERE id = v_store_a;
    EXCEPTION WHEN foreign_key_violation THEN
        v_restrict_failed := TRUE;
    END;

    IF NOT v_restrict_failed THEN
        RAISE EXCEPTION 'Store physical deletion should be blocked by RESTRICT foreign key when orders exist';
    END IF;
    RAISE NOTICE 'SUCCESS: Store physical deletion blocked by RESTRICT constraint protecting order history.';

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
        'midtrans', 'trx-test-12345', 'ORD-TEST-001', '200', 'settlement', 25000, '{"status": "settlement"}'::jsonb
    );

    BEGIN
        INSERT INTO payment_notification_logs (
            provider, transaction_id, order_number, status_code, transaction_status, gross_amount, raw_payload
        ) VALUES (
            'midtrans', 'trx-test-12345', 'ORD-TEST-001', '200', 'settlement', 25000, '{"status": "settlement"}'::jsonb
        );
        RAISE EXCEPTION 'Duplicate webhook insert was permitted!';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE 'SUCCESS: Duplicate webhook notification was safely rejected by unique constraint.';
    END;

    RAISE NOTICE 'ALL CONSTRAINTS AND BEHAVIORAL VERIFICATIONS PASSED SUCCESSFULLY.';
END $$;
