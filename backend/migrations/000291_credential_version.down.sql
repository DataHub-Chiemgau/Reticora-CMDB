-- Reverts 000291: the secret version, the rotation time and the age limit
-- go; the secrets themselves stay.
ALTER TABLE credential
    DROP CONSTRAINT credential_max_age_days_check,
    DROP COLUMN max_age_days,
    DROP COLUMN rotated_at,
    DROP COLUMN secret_version;
