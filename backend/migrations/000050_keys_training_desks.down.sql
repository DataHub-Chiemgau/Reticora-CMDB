DROP TABLE IF EXISTS desk_booking;
DROP TABLE IF EXISTS desk;
DROP TABLE IF EXISTS training_assignment;
DROP TABLE IF EXISTS training;
DROP TABLE IF EXISTS key_assignment;
DROP TABLE IF EXISTS key_item;
DELETE FROM permission WHERE key IN ('key:read','key:write','training:read','training:write','desk:read','desk:write');
