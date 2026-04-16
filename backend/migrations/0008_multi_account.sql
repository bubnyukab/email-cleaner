-- Add account scoping to senders and sender_emails.
ALTER TABLE senders ADD COLUMN IF NOT EXISTS gmail_account_id INT REFERENCES gmail_accounts(id);
ALTER TABLE sender_emails ADD COLUMN IF NOT EXISTS gmail_account_id INT REFERENCES gmail_accounts(id);

-- Backfill: associate existing rows with the first (most recently updated) account.
UPDATE senders SET gmail_account_id = (
    SELECT id FROM gmail_accounts ORDER BY updated_at DESC LIMIT 1
) WHERE gmail_account_id IS NULL;

UPDATE sender_emails SET gmail_account_id = (
    SELECT id FROM gmail_accounts ORDER BY updated_at DESC LIMIT 1
) WHERE gmail_account_id IS NULL;
