DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['subnet', 'ip_address', 'vlan', 'vrf', 'ipam_section'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS trg_%1$s_ipam_change_insert ON %1$I', t);
        EXECUTE format('DROP TRIGGER IF EXISTS trg_%1$s_ipam_change_update ON %1$I', t);
        EXECUTE format('DROP TRIGGER IF EXISTS trg_%1$s_ipam_change_delete ON %1$I', t);
    END LOOP;
END
$$;

DROP FUNCTION IF EXISTS ipam_change_record();
DROP TABLE IF EXISTS ipam_change;
