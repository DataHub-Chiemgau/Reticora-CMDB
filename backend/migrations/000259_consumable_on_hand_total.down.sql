-- Reverts 000259_consumable_on_hand_total (WP-285): the total stock is the
-- sum over stock_level again.

ALTER TABLE consumable DROP CONSTRAINT consumable_on_hand_total_check,
    DROP COLUMN on_hand_total;
