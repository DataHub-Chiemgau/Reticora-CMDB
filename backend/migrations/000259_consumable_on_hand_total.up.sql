-- Migration 000259: total stock of a consumable (STK-01, NTF-05, WP-285,
-- E-128 (a)).
--
-- consumable.on_hand_total holds the sum of stock_level.on_hand over all
-- bins of the article, independent of the scope of the booking or reading
-- principal (stock_level_isolation filters the bins by client and site).
-- Only consumable.ApplyBooking writes it, in the transaction and under the
-- row lock of the consumable that books the bins. The threshold rule of
-- the minimum level reads it (on_hand_total < min_level), and the edge from
-- "not below" to "below" writes the event stock.below_minimum. There is no
-- stored "already reported" state; table, index and RLS stay unchanged.

ALTER TABLE consumable ADD COLUMN on_hand_total NUMERIC(12,2) NOT NULL DEFAULT 0,
    ADD CONSTRAINT consumable_on_hand_total_check CHECK (on_hand_total >= 0);

UPDATE consumable c
   SET on_hand_total = s.total
  FROM (SELECT organization_id, consumable_id, sum(on_hand) AS total
          FROM stock_level
         GROUP BY organization_id, consumable_id) s
 WHERE s.consumable_id = c.id AND s.organization_id = c.organization_id
   AND s.total <> 0;
