### Building and running your application

When you're ready, start your application by running:
`docker compose up --build`.

Services:
- Frontend (Next.js): http://localhost:3000
- Backend (Go API): http://localhost:8080
- Postgres: localhost:5432
- Inbox Overview UI: http://localhost:3000/inbox
- Sender Groups UI: http://localhost:3000/senders

### Gmail OAuth setup

Set these env vars before starting containers:

- `GOOGLE_CLIENT_ID`
- `GOOGLE_CLIENT_SECRET`
- `GOOGLE_REDIRECT_URL` (default: `http://localhost:8080/api/go/auth/google/callback`)

Example PowerShell:

`$env:GOOGLE_CLIENT_ID="your-client-id"`

`$env:GOOGLE_CLIENT_SECRET="your-client-secret"`

### First-time flow

1. Open `http://localhost:3000/inbox`
2. Click `Connect Gmail` (Google OAuth)
3. Click `Sync Inbox`
4. Open `/senders` to inspect sender groups
5. Open `/senders/{id}` to inspect that sender's emails

### Data model

- Backend owns Gmail ingestion and sender-centric storage.
- Frontend reads only from Go API:
  - `GET /api/go/inbox/stats`
  - `GET /api/go/senders`
  - `GET /api/go/senders/{senderId}/emails`
  - `POST /api/go/sync/gmail`

### Deploying your application to the cloud

First, build your image, e.g.: `docker build -t myapp .`.
If your cloud uses a different CPU architecture than your development
machine (e.g., you are on a Mac M1 and your cloud provider is amd64),
you'll want to build the image for that platform, e.g.:
`docker build --platform=linux/amd64 -t myapp .`.

Then, push it to your registry, e.g. `docker push myregistry.com/myapp`.

Consult Docker's [getting started](https://docs.docker.com/go/get-started-sharing/)
docs for more detail on building and pushing.

### References
* [Docker's Go guide](https://docs.docker.com/language/golang/)