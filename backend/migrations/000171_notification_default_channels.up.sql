-- Migration 000171: default in-app channel of existing organizations
-- (WP-509, NTF-01, NTF-04, UI-16; F-157).
--
-- Without a channel the notifier drops every notification of an
-- organization (review, export, job, webhook, ...) until someone creates
-- one through POST /notification-channels. New organizations get their
-- default channels when they are created (signup and operator path,
-- notify.ProvisionDefaultChannels); this backfill gives every existing
-- organization without an in-app channel the in-app channel of the
-- notification center. An organization that has one, enabled or disabled,
-- keeps it as it is; running the backfill again creates nothing. No e-mail
-- channel is created: whether the installation has a central SMTP server is
-- its runtime configuration, not known here.
--
-- The name is the default name "In-App"; when an organization already uses
-- that name for another channel, "In-App (Standard)", and should that be
-- taken too, a unique suffix.

INSERT INTO notification_channel (organization_id, name, kind, provider, enabled)
SELECT o.id,
       CASE
           WHEN NOT EXISTS (SELECT 1 FROM notification_channel c
                             WHERE c.organization_id = o.id AND c.name = 'In-App') THEN 'In-App'
           WHEN NOT EXISTS (SELECT 1 FROM notification_channel c
                             WHERE c.organization_id = o.id AND c.name = 'In-App (Standard)') THEN 'In-App (Standard)'
           ELSE 'In-App (Standard ' || gen_random_uuid()::text || ')'
       END,
       'inapp', 'inapp', true
  FROM organization o
 WHERE NOT EXISTS (SELECT 1 FROM notification_channel c
                    WHERE c.organization_id = o.id AND c.kind = 'inapp');
