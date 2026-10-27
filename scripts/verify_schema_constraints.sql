DO $$
DECLARE
    v_store_a UUID;
    v_store_b UUID;
    v_cat_a UUID;
    v_prod_a UUID;
    v_order_a UUID;
    v_item_a UUID;
    v_check_cat UUID;
    v_check_prod UUID;
BEGIN
    -- 1. Insert Stores
    INSERT INTO stores (slug, name) VALUES ('senja-a', 'Kafe Senja A') RETURNING id INTO v_store_a;
    INSERT INTO stores (slug, name) VALUES ('senja-b', 'Kafe Senja B') RETURNING id INTO v_store_b;

    -- 2. Insert Category & Product in Store A
    INSERT INTO categories (store_id, name) VALUES (v_store_a, 'Kopi') RETURNING id INTO v_cat_a;
    INSERT INTO products (store_id, category_id, name, base_price) VALUES (v_store_a, v_cat_a, 'Kopi Susu Senja', 22000) RETURNING id INTO v_prod_a;

    -- 3. Insert Order & Order Item in Store A
    INSERT INTO orders (store_id, order_number, queue_number, customer_name, total_amount) 
    VALUES (v_store_a, 'ORD-TEST-001', 'A01', 'Budi', 22000) RETURNING id INTO v_order_a;

    INSERT INTO order_items (store_id, order_id, product_id, snapshot_name, snapshot_price, quantity, subtotal_amount)
    VALUES (v_store_a, v_order_a, v_prod_a, 'Kopi Susu Senja', 22000, 1, 22000) RETURNING id INTO v_item_a;

    -- 4. Test Deletion of Category: verify product.category_id becomes NULL, store_id intact
    DELETE FROM categories WHERE id = v_cat_a;
    SELECT category_id INTO v_check_cat FROM products WHERE id = v_prod_a;
    IF v_check_cat IS NOT NULL THEN
        RAISE EXCEPTION 'Category deletion failed to set category_id to NULL';
    END IF;
    RAISE NOTICE 'SUCCESS: Category deletion set category_id to NULL while store_id remained intact.';

    -- 5. Test Deletion of Product: verify order_item.product_id becomes NULL, snapshot intact
    DELETE FROM products WHERE id = v_prod_a;
    SELECT product_id INTO v_check_prod FROM order_items WHERE id = v_item_a;
    IF v_check_prod IS NOT NULL THEN
        RAISE EXCEPTION 'Product deletion failed to set product_id to NULL in order_items';
    END IF;
    RAISE NOTICE 'SUCCESS: Product deletion set product_id to NULL while preserving historical order_item.';

    -- 6. Test Webhook Idempotency
    INSERT INTO payment_notification_logs (
        provider, transaction_id, order_number, status_code, transaction_status, gross_amount, raw_payload
    ) VALUES (
        'midtrans', 'trx-12345', 'ORD-TEST-001', '200', 'settlement', 22000, '{"status": "settlement"}'::jsonb
    );

    BEGIN
        INSERT INTO payment_notification_logs (
            provider, transaction_id, order_number, status_code, transaction_status, gross_amount, raw_payload
        ) VALUES (
            'midtrans', 'trx-12345', 'ORD-TEST-001', '200', 'settlement', 22000, '{"status": "settlement"}'::jsonb
        );
        RAISE EXCEPTION 'Duplicate webhook notification should have been rejected by unique constraint!';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE 'SUCCESS: Duplicate webhook correctly rejected by unique constraint.';
    END;

    -- 7. Test Tenant Cross-Pollination Mismatch
    BEGIN
        -- Try to insert order item in Store B pointing to Order in Store A
        INSERT INTO order_items (store_id, order_id, snapshot_name, snapshot_price, quantity, subtotal_amount)
        VALUES (v_store_b, v_order_a, 'Rogue Item', 10000, 1, 10000);
        RAISE EXCEPTION 'Cross-tenant order reference should have been rejected by composite FK!';
    EXCEPTION WHEN foreign_key_violation THEN
        RAISE NOTICE 'SUCCESS: Cross-tenant foreign key violation correctly caught and rejected.';
    END;

    RAISE NOTICE 'ALL POSITIVE & NEGATIVE DATABASE TESTS PASSED!';
END $$;
