-- Down of 000208: the reservation columns of ip_address go away.
ALTER TABLE ip_address
    DROP COLUMN reserved_until,
    DROP COLUMN reserved_by;
