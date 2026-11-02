-- Reverts 000191: idx_ci_fts with the definition of 000021.

CREATE INDEX IF NOT EXISTS idx_ci_fts ON ci USING GIN (
    to_tsvector('simple',
        coalesce(name,'') || ' ' ||
        coalesce(hostname,'') || ' ' ||
        coalesce(manufacturer,'') || ' ' ||
        coalesce(model,'') || ' ' ||
        coalesce(serial_number,'')
    )
);
