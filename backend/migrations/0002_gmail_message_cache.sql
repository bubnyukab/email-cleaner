CREATE TABLE IF NOT EXISTS gmail_message_cache (
  gmail_message_id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

