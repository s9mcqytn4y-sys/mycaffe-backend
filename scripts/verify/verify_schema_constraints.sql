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
    v_opt_parent UUID;
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

    -- 5. Insert Order, Order Item, Order Item Option (with 1-level Sub-Addon), Split & Payment in Store A and Store B
    INSERT INTO orders (store_id, idempotency_key, request_hash, order_number, queue_number, customer_name, status, payment_status, subtotal_amount, total_amount) 
    VALUES (v_store_a, 'idem-order-a-001', 'req-hash-a-001', 'ORD-TEST-001', 'A01', 'Customer Alpha', 'pending_payment', 'pending', 33000, 33000) RETURNING id INTO v_order_a;

    INSERT INTO orders (store_id, idempotency_key, request_hash, order_number, queue_number, customer_name, status, payment_status, subtotal_amount, total_amount) 
    VALUES (v_store_b, 'idem-order-b-001', 'req-hash-b-001', 'EXP-TEST-001', 'B01', 'Customer Beta', 'pending_payment', 'pending', 25000, 25000) RETURNING id INTO v_order_b;

    INSERT INTO order_items (store_id, order_id, product_id, unit_base_price, modifier_total_price, snapshot_name, snapshot_price, quantity, subtotal_amount)
    VALUES (v_store_a, v_order_a, v_prod_a, 25000, 8000, 'Classic Iced Latte', 33000, 1, 33000) RETURNING id INTO v_item_a;

    INSERT INTO order_item_options (store_id, order_item_id, modifier_group_id, modifier_option_id, snapshot_group_name, snapshot_option_name, snapshot_price_delta, quantity)
    VALUES (v_store_a, v_item_a, v_mod_group, v_mod_opt, 'Milk Option', 'Oat Milk', 8000, 1)
    RETURNING id INTO v_opt_parent;

    INSERT INTO order_item_options (store_id, order_item_id, parent_order_item_option_id, modifier_group_id, modifier_option_id, parent_option_id, snapshot_group_name, snapshot_option_name, snapshot_price_delta, quantity)
    VALUES (v_store_a, v_item_a, v_opt_parent, v_mod_group, v_mod_opt, v_mod_opt, 'Sub Option', 'Cold Foam', 0, 1);

    INSERT INTO payment_splits (store_id, order_id, split_number, label, split_mode, subtotal_amount, total_amount, status)
    VALUES (v_store_a, v_order_a, 1, 'Split 1', 'full', 33000, 33000, 'pending') RETURNING id INTO v_split_a;

    v_payment_a := uuid_generate_v4();
    INSERT INTO payments (id, store_id, order_id, split_id, attempt_no, idempotency_key, provider_order_id, amount, method, status, transaction_id)
    VALUES (v_payment_a, v_store_a, v_order_a, v_split_a, 1, 'idem-test-001', v_payment_a::text, 33000, 'qris', 'pending', 'tx-unique-001');

    -- 5B. Verify Payment Attempt Idempotency Constraint (uq_payments_store_idempotency_key)
    BEGIN
        INSERT INTO payments (store_id, order_id, split_id, attempt_no, idempotency_key, provider_order_id, amount, method, status)
        VALUES (v_store_a, v_order_a, v_split_a, 2, 'idem-test-001', uuid_generate_v4()::text, 33000, 'qris', 'pending');
        RAISE EXCEPTION 'Duplicate payment idempotency_key was permitted!';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE 'SUCCESS: Duplicate payment idempotency_key rejected by uq_payments_store_idempotency_key.';
    END;

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

    -- 9. Test webhook idempotency using the durable (store_id, event_hash) inbox key.
    INSERT INTO payment_notification_logs (
        store_id, order_id, provider, provider_order_id, event_hash, transaction_id,
        order_number, status_code, transaction_status, gross_amount, raw_payload
    ) VALUES (
        v_store_a, v_order_a, 'midtrans', v_payment_a::text, repeat('a', 64),
        'trx-test-12345', 'ORD-TEST-001-S1-A1', '200', 'settlement', 33000,
        '{"status": "settlement"}'::jsonb
    );

    BEGIN
        INSERT INTO payment_notification_logs (
            store_id, order_id, provider, provider_order_id, event_hash, transaction_id,
            order_number, status_code, transaction_status, gross_amount, raw_payload
        ) VALUES (
            v_store_a, v_order_a, 'midtrans', v_payment_a::text, repeat('a', 64),
            'trx-test-12345', 'ORD-TEST-001-S1-A1', '200', 'settlement', 33000,
            '{"status": "settlement"}'::jsonb
        );
        RAISE EXCEPTION 'Duplicate webhook insert was permitted!';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE 'SUCCESS: Duplicate webhook event rejected by uq_payment_notification_logs_event_hash.';
    END;
END $$;

-- 10. Test Row-Level Security (RLS) & Financial DELETE Revocation under mycaffe_app
DO $$
DECLARE
    v_store_a UUID;
    v_store_b UUID;
    v_resolved_store UUID;
    v_provider_order_id VARCHAR(100);
    v_visible_stores INT;
    v_visible_orders INT;
    v_visible_notifs INT;
    v_rls_blocked BOOLEAN := FALSE;
    v_delete_payment_blocked BOOLEAN := FALSE;
    v_delete_order_blocked BOOLEAN := FALSE;
    v_delete_notif_blocked BOOLEAN := FALSE;
BEGIN
    SELECT id INTO v_store_a FROM stores WHERE slug = 'demo-store-a';
    SELECT id INTO v_store_b FROM stores WHERE slug = 'demo-store-b';

    -- Resolve the provider ID before switching to the app role; RLS intentionally hides
    -- payment rows until a tenant context is set.
    SELECT provider_order_id INTO v_provider_order_id
    FROM payments
    WHERE store_id = v_store_a AND idempotency_key = 'idem-test-001';

    -- Switch to runtime application role (NOBYPASSRLS)
    EXECUTE 'SET LOCAL ROLE mycaffe_app';

    -- 10A. Verify controlled SECURITY DEFINER webhook context resolver works before setting tenant context
    SELECT store_id INTO v_resolved_store FROM resolve_payment_webhook_context(v_provider_order_id);
    IF v_resolved_store IS DISTINCT FROM v_store_a THEN
        RAISE EXCEPTION 'resolve_payment_webhook_context failed: expected %, got %', v_store_a, v_resolved_store;
    END IF;

    -- 10B. Without setting app.current_store_id, 0 stores, 0 orders, and 0 notification logs must be visible
    SELECT COUNT(*) INTO v_visible_stores FROM stores;
    SELECT COUNT(*) INTO v_visible_orders FROM orders;
    SELECT COUNT(*) INTO v_visible_notifs FROM payment_notification_logs;
    IF v_visible_stores != 0 OR v_visible_orders != 0 OR v_visible_notifs != 0 THEN
        RAISE EXCEPTION 'RLS leak when context unset: stores=%, orders=%, notifs=%', v_visible_stores, v_visible_orders, v_visible_notifs;
    END IF;

    -- 10C. Set tenant context to Store A
    PERFORM set_config('app.current_store_id', v_store_a::text, true);

    SELECT COUNT(*) INTO v_visible_stores FROM stores;
    SELECT COUNT(*) INTO v_visible_orders FROM orders;
    SELECT COUNT(*) INTO v_visible_notifs FROM payment_notification_logs;
    IF v_visible_stores != 1 OR v_visible_orders != 1 OR v_visible_notifs != 1 THEN
        RAISE EXCEPTION 'RLS error for Store A: stores=%, orders=%, notifs=%', v_visible_stores, v_visible_orders, v_visible_notifs;
    END IF;

    -- 10D. Verify mycaffe_app CANNOT physically DELETE payments, orders, or payment_notification_logs
    BEGIN
        DELETE FROM payments;
    EXCEPTION WHEN insufficient_privilege THEN
        v_delete_payment_blocked := TRUE;
    END;
    IF NOT v_delete_payment_blocked THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app was allowed to DELETE FROM payments!';
    END IF;

    BEGIN
        DELETE FROM orders;
    EXCEPTION WHEN insufficient_privilege THEN
        v_delete_order_blocked := TRUE;
    END;
    IF NOT v_delete_order_blocked THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app was allowed to DELETE FROM orders!';
    END IF;

    BEGIN
        DELETE FROM payment_notification_logs;
    EXCEPTION WHEN insufficient_privilege THEN
        v_delete_notif_blocked := TRUE;
    END;
    IF NOT v_delete_notif_blocked THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app was allowed to DELETE FROM payment_notification_logs!';
    END IF;

    -- 10E. Attempt cross-tenant insert into Store B while scoped to Store A (must fail WITH CHECK policy)
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

    -- 10F. Staff auth sessions (migration 000005): RLS enabled, runtime role cannot DELETE,
    --      and the pre-auth resolver is not executable by PUBLIC.
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.auth_sessions'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: auth_sessions does not have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.auth_sessions', 'DELETE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may DELETE auth_sessions (sessions must be revoked, not deleted)!';
    END IF;
    IF NOT has_function_privilege('mycaffe_app', 'public.resolve_store_for_login(character varying)', 'EXECUTE') THEN
        RAISE EXCEPTION 'resolve_store_for_login is not executable by mycaffe_app';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
        WHERE p.proname = 'resolve_store_for_login' AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'
    ) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: resolve_store_for_login is executable by PUBLIC!';
    END IF;

    -- 10F-2. Tables and table sessions (migration 000016): RLS on, no DELETE, QR resolver private.
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.tables'::regclass)
       OR NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.table_sessions'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: tables/table_sessions do not have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.tables', 'DELETE')
       OR has_table_privilege('mycaffe_app', 'public.table_sessions', 'DELETE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may DELETE tables or table_sessions (archive/close instead)!';
    END IF;
    IF NOT has_function_privilege('mycaffe_app', 'public.resolve_table_qr(character varying)', 'EXECUTE') THEN
        RAISE EXCEPTION 'resolve_table_qr is not executable by mycaffe_app';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
        WHERE p.proname = 'resolve_table_qr' AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'
    ) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: resolve_table_qr is executable by PUBLIC!';
    END IF;

    -- 10F-3. Guest sessions and the public tracker (migration 000017).
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.guest_sessions'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: guest_sessions does not have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.guest_sessions', 'DELETE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may DELETE guest_sessions!';
    END IF;
    IF NOT has_function_privilege('mycaffe_app', 'public.track_order_by_token(bytea)', 'EXECUTE') THEN
        RAISE EXCEPTION 'track_order_by_token is not executable by mycaffe_app';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
        WHERE p.proname = 'track_order_by_token' AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'
    ) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: track_order_by_token is executable by PUBLIC!';
    END IF;

    -- 10F-4. Outbox relay and WebSocket tickets (migration 000018).
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.ws_tickets'::regclass)
       OR NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.store_event_sequences'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: ws_tickets/store_event_sequences do not have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.ws_tickets', 'DELETE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may DELETE ws_tickets!';
    END IF;
    IF NOT has_function_privilege('mycaffe_app', 'public.list_stores_with_pending_outbox(integer)', 'EXECUTE') THEN
        RAISE EXCEPTION 'list_stores_with_pending_outbox is not executable by mycaffe_app';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
        WHERE p.proname = 'list_stores_with_pending_outbox' AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'
    ) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: list_stores_with_pending_outbox is executable by PUBLIC!';
    END IF;
    -- 10F-5. KDS tickets (migration 000019).
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.kds_tickets'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: kds_tickets does not have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.kds_tickets', 'DELETE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may DELETE kds_tickets!';
    END IF;
    -- 10F-6. Receipt tokens (migration 000020): RLS on, insert-only for the app role.
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.receipt_tokens'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: receipt_tokens does not have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.receipt_tokens', 'DELETE')
       OR has_table_privilege('mycaffe_app', 'public.receipt_tokens', 'UPDATE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may UPDATE or DELETE receipt_tokens!';
    END IF;
    IF EXISTS (SELECT 1 FROM outbox_events WHERE store_seq IS NULL) THEN
        RAISE EXCEPTION 'outbox_events rows without store_seq';
    END IF;

    -- 10G. Account tokens and email outbox (migration 000006): RLS on, no DELETE, discovery
    --      function not executable by PUBLIC.
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.account_tokens'::regclass)
       OR NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.email_outbox'::regclass)
       OR NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.store_settings'::regclass)
       OR NOT (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.product_variants'::regclass) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: account_tokens/email_outbox/store_settings must have row-level security enabled!';
    END IF;
    IF has_table_privilege('mycaffe_app', 'public.account_tokens', 'DELETE')
       OR has_table_privilege('mycaffe_app', 'public.email_outbox', 'DELETE')
       OR has_table_privilege('mycaffe_app', 'public.product_variants', 'DELETE') THEN
        RAISE EXCEPTION 'SECURITY FAILURE: mycaffe_app may DELETE account_tokens/email_outbox!';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
        WHERE p.proname = 'list_stores_with_due_email' AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'
    ) THEN
        RAISE EXCEPTION 'SECURITY FAILURE: list_stores_with_due_email is executable by PUBLIC!';
    END IF;

    RAISE NOTICE 'SUCCESS: Row-Level Security (RLS) on stores/orders/payments/notifications and DELETE privilege revocation verified.';
    RAISE NOTICE 'ALL CONSTRAINTS, RLS POLICIES, AND BEHAVIORAL VERIFICATIONS PASSED SUCCESSFULLY.';
END $$;
