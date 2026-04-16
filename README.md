# Email Cleaner

A self-hosted Gmail inbox management tool that helps you identify high-volume senders, bulk-trash unwanted emails, and unsubscribe from mailing lists — all from a clean web interface.

## Features

- Sync your Gmail inbox and group emails by sender
- Bulk-trash all emails from selected senders in one click
- Unsubscribe from senders directly (uses List-Unsubscribe header)
- Analytics dashboard: top senders, email timeline, label breakdown
- Export sender data to CSV
- Block senders via Gmail Filters API
- Per-sender email preview modal with HTML rendering
- Label/category filtering, sorting, and search
- Dark mode with system preference detection
- Responsive mobile layout

## Architecture

```
┌─────────────────┐       ┌──────────────────┐       ┌─────────────┐
│  Next.js 15     │──────▶│  Go API (Gorilla) │──────▶│  PostgreSQL │
│  (frontend)     │ HTTP  │  :8080            │  SQL  │             │
│  :3000          │       │  backend/main.go  │       └─────────────┘
└─────────────────┘       └────────┬─────────┘
                                   │ Gmail API
                                   ▼
                           ┌──────────────────┐
                           │  Gmail (OAuth2)   │
                           └──────────────────┘
```

## Prerequisites

- Go 1.26+
- Node.js 20+
- PostgreSQL 15+
- A Google Cloud project with the Gmail API enabled

## Setup

### 1. Google OAuth credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application)
3. Add `http://localhost:8080/api/go/auth/google/callback` as an Authorized redirect URI
4. Enable the Gmail API for your project

### 2. Environment variables

Copy `.env.example` to `.env` and fill in your values:

```bash
cp .env.example .env
```

| Variable | Description |
|---|---|
| `GOOGLE_CLIENT_ID` | OAuth client ID from Google Cloud Console |
| `GOOGLE_CLIENT_SECRET` | OAuth client secret |
| `GOOGLE_REDIRECT_URL` | Must match the redirect URI registered in Google Cloud Console |
| `DATABASE_URL` | PostgreSQL connection string |
| `FRONTEND_URL` | URL of the Next.js frontend (used for post-auth redirect) |
| `BACKEND_URL` | Backend URL for server-side Next.js fetches |
| `NEXT_PUBLIC_BACKEND_URL` | Backend URL exposed to the browser |

### 3. Run with Docker Compose (recommended)

```bash
docker compose up
```

The app will be available at `http://localhost:3000`.

### 4. Run locally

**Backend:**

```bash
cd backend
go run .
```

**Frontend:**

```bash
cd frontend
npm install
npm run dev
```

## Usage

1. Open `http://localhost:3000/inbox`
2. Click **Connect Gmail** and authorize the app
3. Click **Sync Inbox** to fetch your emails (this may take a few minutes for large inboxes)
4. Navigate to **Sender Groups** to see emails grouped by sender
5. Select senders and click **Move to Trash** to bulk-clean your inbox

## Development

### Database migrations

SQL migration files live in `backend/migrations/`. They are run automatically on startup in alphabetical order. Each migration is applied exactly once.

### Backend

The backend is a single Go file (`backend/main.go`) using:
- [`gorilla/mux`](https://github.com/gorilla/mux) for routing
- [`lib/pq`](https://github.com/lib/pq) for PostgreSQL
- [`golang.org/x/oauth2`](https://pkg.go.dev/golang.org/x/oauth2) for Gmail OAuth
- [`google.golang.org/api/gmail/v1`](https://pkg.go.dev/google.golang.org/api/gmail/v1) for the Gmail API

### Frontend

The frontend is a Next.js 15 App Router application using:
- Tailwind CSS v4
- shadcn/ui component primitives (Radix UI)
- Sonner for toast notifications
- Recharts for analytics charts
- Lucide React for icons
