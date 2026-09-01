DROP TABLE IF EXISTS internal_order_item;
DROP TABLE IF EXISTS internal_order;
DELETE FROM permission WHERE key IN ('order:read','order:write','order:approve');
