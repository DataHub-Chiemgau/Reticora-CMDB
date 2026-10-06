-- WP-076 (AI-02): external AI processing (embeddings) of an organization's
-- data needs the organization's opt-in in addition to the ai add-on.
ALTER TABLE organization ADD COLUMN ai_opt_in boolean NOT NULL DEFAULT false;
