ALTER TABLE senders ADD COLUMN IF NOT EXISTS unsubscribe_url TEXT;
ALTER TABLE senders ADD COLUMN IF NOT EXISTS unsubscribe_mailto TEXT;
ALTER TABLE senders ADD COLUMN IF NOT EXISTS unsubscribed_at TIMESTAMPTZ;

ALTER TABLE sender_emails ADD COLUMN IF NOT EXISTS list_unsubscribe TEXT;
