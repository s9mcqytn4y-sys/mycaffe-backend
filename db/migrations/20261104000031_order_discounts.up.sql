-- 20261104000031_order_discounts.up.sql
-- v0.8 PR 8.6 (V12, ADR-007): an order-level discount on an unpaid pay-first order. Above the store's
-- cashier_max_discount_bps it needs the owner (own PIN or an approved 'discount' request); a 100 %
-- discount is a compliment and settles the order without a payment.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS discount_reason TEXT;

-- A compliment leaves a zero-total split, settled without a payment.
ALTER TABLE payment_splits DROP CONSTRAINT IF EXISTS chk_payment_splits_amounts;
ALTER TABLE payment_splits ADD CONSTRAINT chk_payment_splits_amounts CHECK (subtotal_amount >= 0 AND tax_amount >= 0
    AND service_charge_amount >= 0 AND total_amount >= 0 AND paid_amount >= 0 AND paid_amount <= total_amount);

ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS chk_approval_requests_action;
ALTER TABLE approval_requests ADD CONSTRAINT chk_approval_requests_action CHECK (
    action IN ('void', 'refund', 'stock_override', 'shift_variance', 'outlet_expense', 'discount'));

ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS chk_audit_logs_action;
ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_logs_action CHECK (
    action IN (
        'VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE',
        'SHIFT_OPEN', 'SHIFT_CLOSE', 'SHIFT_FORCE_CLOSE',
        'ROLE_CHANGE', 'STATIC_QRIS_RECONCILE', 'CASH_PAYMENT_RECORDED', 'EDC_RECONCILE',
        'ORDER_CREATE', 'ORDER_CANCEL', 'PAYMENT_SETTLED',
        'STAFF_LOGIN_LOCKED', 'STAFF_UNLOCK', 'STAFF_PIN_RESET', 'STAFF_DEACTIVATED',
        'STAFF_SESSION_REVOKED', 'STAFF_INVITED', 'STAFF_PIN_SET', 'STAFF_TEMP_PIN_ISSUED',
        'STORE_BOOTSTRAP', 'STORE_SETTINGS_CHANGED',
        'CASH_MOVEMENT', 'DRAWER_CREATE', 'DRAWER_UPDATE',
        'TAB_OPEN', 'ROUND_SUBMIT', 'TAB_CLOSE', 'TAB_REOPEN',
        'OWNER_PIN_FAILED',
        'TABLE_CREATE', 'TABLE_UPDATE', 'TABLE_QR_ROTATE', 'TABLE_SESSION_CLOSE',
        'PRODUCT_IMAGE_CHANGED',
        'APPROVAL_REQUESTED', 'APPROVAL_APPROVED', 'APPROVAL_REJECTED',
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED',
        'OUTLET_CASH_MOVEMENT',
        'ORDER_DISCOUNT'
    )
);
