# Email Cleaner Frontend

Minimal Next.js UI for a sender-centric Gmail cleaner.

## Routes

- `/inbox`: overview cards (emails scanned, unique senders, connected accounts) and sync actions
- `/senders`: sender groups with email/thread counts
- `/senders/[id]`: email list for a single sender

## Environment

Create `.env` from `.env.example`:

```bash
cp .env.example .env
```

Default:

```env
BACKEND_URL=http://localhost:8080
```

## Local development

```bash
bun install
bun run dev
```
