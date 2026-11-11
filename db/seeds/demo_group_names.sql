-- Demo data only (PRD v3 N7), applied to the demo outlet on 2026-11-11 after an R2 backup.
-- Run with psql as the migrator: it renames 8 groups or rolls back. Not part of migrations or test fixtures.
-- The map below holds both names; swap the columns to roll back.
BEGIN;
SELECT set_config('app.current_store_id', '11111111-1111-4111-8111-111111111111', true);
CREATE TEMP TABLE n7_map (old_name text, new_name text) ON COMMIT DROP;
INSERT INTO n7_map VALUES
  ('Bakery Serving & Spread', 'Serving & Spread'),
  ('Espresso & Bean Upgrade', 'Extra Shot & Beans'),
  ('Ice Cube Level', 'Ice Level'),
  ('Kitchen Add-Ons & Spice Preference', 'Sides & Spice'),
  ('Milk Selection', 'Milk'),
  ('Plant Milk Preparation (Sub-Addon)', 'Plant Milk Topping'),
  ('Roastery Grind Size', 'Grind Size'),
  ('Sweetness Level', 'Sweetness');
DO $$
DECLARE n int;
BEGIN
  UPDATE modifier_groups g SET name = m.new_name, updated_at = NOW()
  FROM n7_map m
  WHERE g.store_id = '11111111-1111-4111-8111-111111111111' AND g.name = m.old_name AND g.archived_at IS NULL;
  GET DIAGNOSTICS n = ROW_COUNT;
  IF n <> 8 THEN RAISE EXCEPTION 'expected 8 groups renamed, got %', n; END IF;
END $$;
COMMIT;
SELECT name FROM modifier_groups WHERE store_id = '11111111-1111-4111-8111-111111111111' AND archived_at IS NULL ORDER BY name;
