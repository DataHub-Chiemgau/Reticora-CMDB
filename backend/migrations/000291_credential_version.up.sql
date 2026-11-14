-- Migration 000291: version, rotation time and maximum age of a stored
-- secret (WP-889, CRD-05). PUT /credentials/{id}/secret replaces the secret
-- and raises secret_version, rotated_at is the time of the last
-- replacement; max_age_days (1–3650, NULL: none) is the age limit WP-897
-- checks. key_version stays the version of the organization's DEK.
ALTER TABLE credential
    ADD COLUMN secret_version integer NOT NULL DEFAULT 1,
    ADD COLUMN rotated_at timestamptz NULL,
    ADD COLUMN max_age_days integer NULL,
    ADD CONSTRAINT credential_max_age_days_check CHECK (max_age_days BETWEEN 1 AND 3650);
