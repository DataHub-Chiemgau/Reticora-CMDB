DROP TABLE IF EXISTS ai_chunk;
DROP TABLE IF EXISTS ai_message;
DROP TABLE IF EXISTS ai_conversation;
DROP TABLE IF EXISTS search_document;
DELETE FROM permission WHERE key IN ('search:write', 'ai:read');
