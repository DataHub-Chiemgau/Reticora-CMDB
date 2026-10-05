-- WP-075 (CH14, ENT-06): IGA and AI are add-ons with their own entitlement
-- (feature keys iga and ai); no plan includes them. The AI key of ENT-06 is
-- ai (formerly ai_assistant). Existing rows were explicit grants (plans
-- were never stored as rows before WP-072), so they are kept.
UPDATE entitlement SET feature_key = 'ai' WHERE feature_key = 'ai_assistant';
