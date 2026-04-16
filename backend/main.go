package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/lib/pq"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"github.com/joho/godotenv"
)

type api struct {
	db          *sql.DB
	oauthConfig *oauth2.Config
	stateStore  map[string]time.Time
	stateMu     sync.Mutex
	syncMu      sync.Mutex
	syncStatus  syncStatus
}

type syncStatus struct {
	Running      bool      `json:"running"`
	Scanned      int       `json:"scanned"`      // processed uncached messages (new work)
	Checked      int64     `json:"checked"`      // inbox message ids evaluated (cached + uncached)
	PendingTotal int64     `json:"pendingTotal"` // uncached message ids discovered so far
	Total        int64     `json:"total"`        // inbox message count estimate
	Inserted     int       `json:"inserted"`
	Failed       int       `json:"failed"`
	ConnectedAs  string    `json:"connectedAs,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitempty"`
	FinishedAt   time.Time `json:"finishedAt,omitempty"`
}

type inboxStats struct {
	TotalEmails   int `json:"totalEmails"`
	TotalSenders  int `json:"totalSenders"`
	ConnectedApps int `json:"connectedAccounts"`
}

type senderSummary struct {
	ID             int        `json:"id"`
	Email          string     `json:"email"`
	DisplayName    string     `json:"displayName"`
	EmailCount     int        `json:"emailCount"`
	ThreadCount    int        `json:"threadCount"`
	LastReceivedAt *time.Time `json:"lastReceivedAt"`
}

type senderEmail struct {
	ID             int        `json:"id"`
	GmailMessageID string     `json:"gmailMessageId"`
	GmailThreadID  string     `json:"gmailThreadId"`
	Subject        string     `json:"subject"`
	Snippet        string     `json:"snippet"`
	BodyText       string     `json:"bodyText"`
	ReceivedAt     *time.Time `json:"receivedAt"`
}

const (
	gmailRetryMaxAttempts = 8
	gmailRetryBaseDelay   = 500 * time.Millisecond
	gmailRetryMaxDelay    = 15 * time.Second
)

func main() {
	loadEnv()

	db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db, "migrations"); err != nil {
		log.Fatal(err)
	}

	oauthConfig := &oauth2.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		// Bulk actions require modifying labels / deleting messages.
		Scopes:       []string{gmail.GmailModifyScope},
		Endpoint:     google.Endpoint,
	}

	app := &api{
		db:          db,
		oauthConfig: oauthConfig,
		stateStore:  map[string]time.Time{},
	}

	router := mux.NewRouter()
	router.HandleFunc("/api/go/health", app.health).Methods("GET")
	router.HandleFunc("/api/go/auth/google/start", app.googleAuthStart).Methods("GET")
	router.HandleFunc("/api/go/auth/google/callback", app.googleAuthCallback).Methods("GET")
	router.HandleFunc("/api/go/sync/gmail", app.syncGmail).Methods("POST")
	router.HandleFunc("/api/go/sync/status", app.getSyncStatus).Methods("GET")
	router.HandleFunc("/api/go/inbox/stats", app.getInboxStats).Methods("GET")
	router.HandleFunc("/api/go/senders", app.getSenders).Methods("GET")
	router.HandleFunc("/api/go/senders/{senderId}/emails", app.getSenderEmails).Methods("GET")
	router.HandleFunc("/api/go/emails/bulk/trash", app.bulkTrashEmails).Methods("POST")
	router.HandleFunc("/api/go/emails/bulk/delete", app.bulkDeleteEmails).Methods("POST")

	log.Fatal(http.ListenAndServe(":8080", enableCORS(jsonContentTypeMiddleware(router))))
}

func loadEnv() {
	// Try common locations so running from repo root or backend/ works.
	paths := []string{
		".env",
		filepath.Join("backend", ".env"),
		filepath.Join("..", ".env"),
	}
	if err := godotenv.Load(paths...); err != nil {
		log.Printf("warning: .env file not loaded from common paths; using process env only")
	}

	// Docker Compose may inject GOOGLE_* as empty strings. In that case,
	// godotenv.Load will not override them, so fill only missing/empty OAuth keys
	// from .env explicitly.
	fillEmptyEnvFromDotenv(paths, []string{
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
		"GOOGLE_REDIRECT_URL",
	})
}

func fillEmptyEnvFromDotenv(paths []string, keys []string) {
	for _, path := range paths {
		values, err := godotenv.Read(path)
		if err != nil {
			continue
		}
		for _, key := range keys {
			if strings.TrimSpace(os.Getenv(key)) != "" {
				continue
			}
			if v := strings.TrimSpace(values[key]); v != "" {
				_ = os.Setenv(key, v)
			}
		}
	}
}

func runMigrations(db *sql.DB, migrationsDir string) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read migrations dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		var exists bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)", entry.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}

		sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}

		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(name) VALUES ($1)", entry.Name()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (a *api) googleAuthStart(w http.ResponseWriter, r *http.Request) {
	if a.oauthConfig.ClientID == "" || a.oauthConfig.ClientSecret == "" || a.oauthConfig.RedirectURL == "" {
		http.Error(w, `{"error":"missing Google OAuth env vars"}`, http.StatusBadRequest)
		return
	}

	state := randomState()
	a.stateMu.Lock()
	a.stateStore[state] = time.Now().Add(10 * time.Minute)
	a.stateMu.Unlock()

	url := a.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func (a *api) googleAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if !a.consumeState(state) {
		http.Error(w, `{"error":"invalid or expired state"}`, http.StatusBadRequest)
		return
	}

	token, err := a.oauthConfig.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, `{"error":"failed exchanging auth code"}`, http.StatusBadGateway)
		return
	}

	service, err := gmail.NewService(
		r.Context(),
		option.WithTokenSource(a.oauthConfig.TokenSource(r.Context(), token)),
	)
	if err != nil {
		http.Error(w, `{"error":"failed creating gmail service"}`, http.StatusBadGateway)
		return
	}

	profile, err := service.Users.GetProfile("me").Do()
	if err != nil {
		http.Error(w, `{"error":"failed loading gmail profile"}`, http.StatusBadGateway)
		return
	}

	_, err = a.db.Exec(`
		INSERT INTO gmail_accounts (email, access_token, refresh_token, token_expiry, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT(email)
		DO UPDATE SET
			access_token = EXCLUDED.access_token,
			refresh_token = COALESCE(EXCLUDED.refresh_token, gmail_accounts.refresh_token),
			token_expiry = EXCLUDED.token_expiry,
			updated_at = NOW()
	`, profile.EmailAddress, token.AccessToken, token.RefreshToken, token.Expiry)
	if err != nil {
		http.Error(w, `{"error":"failed saving account token"}`, http.StatusInternalServerError)
		return
	}

	frontendURL := strings.TrimSpace(os.Getenv("FRONTEND_URL"))
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}
	frontendURL = strings.TrimRight(frontendURL, "/")
	http.Redirect(w, r, frontendURL+"/inbox", http.StatusSeeOther)
}

func (a *api) syncGmail(w http.ResponseWriter, r *http.Request) {
	a.syncMu.Lock()
	if a.syncStatus.Running {
		a.syncMu.Unlock()
		http.Error(w, `{"error":"sync already running"}`, http.StatusConflict)
		return
	}
	a.syncStatus = syncStatus{
		Running:   true,
		StartedAt: time.Now(),
	}
	a.syncMu.Unlock()

	defer func() {
		a.syncMu.Lock()
		a.syncStatus.Running = false
		a.syncStatus.FinishedAt = time.Now()
		a.syncMu.Unlock()
	}()

	accountEmail, token, err := a.loadPrimaryToken()
	if err != nil {
		a.setSyncError(err.Error())
		http.Error(w, `{"error":"connect Gmail first via /api/go/auth/google/start"}`, http.StatusBadRequest)
		return
	}
	a.setSyncConnectedAccount(accountEmail)

	// Backfill the new cache table from already-stored rows so we can skip
	// older messages immediately after deploying this change.
	_, err = a.db.Exec(`
		INSERT INTO gmail_message_cache (gmail_message_id)
		SELECT gmail_message_id FROM sender_emails
		ON CONFLICT(gmail_message_id) DO NOTHING
	`)
	if err != nil {
		a.setSyncError(err.Error())
		http.Error(w, `{"error":"failed bootstrapping message cache"}`, http.StatusBadGateway)
		return
	}

	ctx := context.Background()
	tokenSource := a.oauthConfig.TokenSource(ctx, token)
	currentToken, err := tokenSource.Token()
	if err == nil {
		_, _ = a.db.Exec(`
			UPDATE gmail_accounts
			SET access_token = $1,
			    refresh_token = COALESCE($2, refresh_token),
			    token_expiry = $3,
			    updated_at = NOW()
			WHERE email = $4
		`, currentToken.AccessToken, currentToken.RefreshToken, currentToken.Expiry, accountEmail)
	}

	service, err := gmail.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		a.setSyncError(err.Error())
		http.Error(w, `{"error":"failed creating gmail service"}`, http.StatusBadGateway)
		return
	}

	// Get the exact total message count from Gmail profile so the dashboard
	// number matches what Gmail shows.
	profile, err := withGmailRetry(func() (*gmail.Profile, error) {
		return service.Users.GetProfile("me").Do()
	})
	if err == nil && profile.MessagesTotal > 0 {
		a.syncMu.Lock()
		a.syncStatus.Total = int64(profile.MessagesTotal)
		a.syncMu.Unlock()
	}

	inserted := 0
	failed := 0
	fetched := 0
	pageToken := ""
	allScannedIDs := make([]string, 0, 10000)
	messageIDs := make(chan string, 2000)
	var workers sync.WaitGroup
	workerCount := runtime.NumCPU() * 2
	if workerCount < 2 {
		workerCount = 2
	}
	if workerCount > 8 {
		workerCount = 8
	}

	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range messageIDs {
				msg, err := withGmailRetry(func() (*gmail.Message, error) {
					return service.Users.Messages.Get("me", id).Format("full").Do()
				})
				if err != nil {
					a.bumpSyncProgress(0, 0, 0, 1, 0, 1)
					continue
				}

				ok, storeErr := a.storeGmailMessage(msg)
				if storeErr != nil {
					a.bumpSyncProgress(0, 0, 0, 1, 0, 1)
					continue
				}

				// Mark as cached even if we couldn't associate the sender, so we
				// don't fetch it again on the next sync run.
				if err := a.markGmailMessageCached(msg.Id); err != nil {
					a.bumpSyncProgress(0, 0, 0, 1, 0, 1)
					continue
				}

				if ok {
					a.bumpSyncProgress(0, 0, 0, 1, 1, 0)
				} else {
					a.bumpSyncProgress(0, 0, 0, 1, 0, 0)
				}
			}
		}()
	}

	for {
		// List all messages without a query filter so the total matches Gmail's
		// reported messagesTotal from GetProfile.
		req := service.Users.Messages.List("me").MaxResults(500)
		if pageToken != "" {
			req = req.PageToken(pageToken)
		}

		listResp, err := withGmailRetry(func() (*gmail.ListMessagesResponse, error) {
			return req.Do()
		})
		if err != nil {
			a.setSyncError(err.Error())
			close(messageIDs)
			workers.Wait()
			http.Error(w, `{"error":"failed listing messages after retries"}`, http.StatusBadGateway)
			return
		}

		// ResultSizeEstimate is unreliable; we already set the real total
		// from GetProfile above.

		ids := make([]string, 0, len(listResp.Messages))
		for _, m := range listResp.Messages {
			ids = append(ids, m.Id)
		}
		allScannedIDs = append(allScannedIDs, ids...)
		fetched += len(ids)

		if len(ids) > 0 {
			// Skip message bodies we've already processed.
			rows, err := a.db.Query(`
				SELECT gmail_message_id
				FROM gmail_message_cache
				WHERE gmail_message_id = ANY($1)
			`, pq.Array(ids))
			if err != nil {
				a.setSyncError(err.Error())
				close(messageIDs)
				workers.Wait()
				http.Error(w, `{"error":"failed querying message cache"}`, http.StatusBadGateway)
				return
			}

			existing := make(map[string]struct{}, len(ids))
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					a.setSyncError(err.Error())
					close(messageIDs)
					workers.Wait()
					http.Error(w, `{"error":"failed reading message cache rows"}`, http.StatusBadGateway)
					return
				}
				existing[id] = struct{}{}
			}
			rows.Close()

			missing := make([]string, 0, len(ids))
			for _, id := range ids {
				if _, ok := existing[id]; !ok {
					missing = append(missing, id)
				}
			}

			// Checked: all ids in this Gmail page.
			// Pending: ids that actually need full fetch + store.
			a.bumpSyncProgress(0, int64(len(ids)), int64(len(missing)), 0, 0, 0)

			for _, id := range missing {
				messageIDs <- id
			}
		}

		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	close(messageIDs)
	workers.Wait()

	// Keep local counts aligned with the latest scanned snapshot so totals
	// don't accumulate stale rows between sync runs.
	if err := a.reconcileScannedSnapshot(allScannedIDs); err != nil {
		a.setSyncError(err.Error())
		http.Error(w, `{"error":"failed reconciling scanned snapshot"}`, http.StatusBadGateway)
		return
	}

	// Keep the authoritative total from GetProfile; allScannedIDs may
	// differ slightly if messages arrive or are deleted during the run.

	a.syncMu.Lock()
	inserted = a.syncStatus.Inserted
	failed = a.syncStatus.Failed
	a.syncMu.Unlock()

	json.NewEncoder(w).Encode(map[string]any{
		"success":       true,
		"connectedAs":   accountEmail,
		"fetched":       fetched,
		"insertedCount": inserted,
		"failedCount":   failed,
	})
}

func (a *api) getSyncStatus(w http.ResponseWriter, r *http.Request) {
	a.syncMu.Lock()
	status := a.syncStatus
	a.syncMu.Unlock()
	json.NewEncoder(w).Encode(status)
}

func withGmailRetry[T any](operation func() (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt < gmailRetryMaxAttempts; attempt++ {
		result, err := operation()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableGmailError(err) || attempt == gmailRetryMaxAttempts-1 {
			break
		}

		time.Sleep(gmailRetryDelay(err, attempt))
	}

	return zero, lastErr
}

func isRetryableGmailError(err error) bool {
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		switch gErr.Code {
		case http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		}
	}
	return false
}

func gmailRetryDelay(err error, attempt int) time.Duration {
	var gErr *googleapi.Error
	if errors.As(err, &gErr) && gErr.Header != nil {
		if retryAfter := gErr.Header.Get("Retry-After"); retryAfter != "" {
			if seconds, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil && seconds > 0 {
				if seconds > gmailRetryMaxDelay {
					return gmailRetryMaxDelay
				}
				return seconds
			}
		}
	}

	delay := gmailRetryBaseDelay * time.Duration(1<<attempt)
	if delay > gmailRetryMaxDelay {
		return gmailRetryMaxDelay
	}
	return delay
}

func (a *api) setSyncConnectedAccount(email string) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.syncStatus.ConnectedAs = email
}

func (a *api) setSyncError(err string) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.syncStatus.LastError = err
}

func (a *api) bumpSyncProgress(
	totalEstimate int64,
	checkedDelta int64,
	pendingTotalDelta int64,
	scannedDelta int,
	insertedDelta int,
	failedDelta int,
) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if totalEstimate > a.syncStatus.Total {
		a.syncStatus.Total = totalEstimate
	}
	a.syncStatus.Checked += checkedDelta
	a.syncStatus.PendingTotal += pendingTotalDelta
	a.syncStatus.Scanned += scannedDelta
	a.syncStatus.Inserted += insertedDelta
	a.syncStatus.Failed += failedDelta
}

func (a *api) markGmailMessageCached(messageID string) error {
	_, err := a.db.Exec(`
		INSERT INTO gmail_message_cache (gmail_message_id)
		VALUES ($1)
		ON CONFLICT(gmail_message_id) DO NOTHING
	`, messageID)
	return err
}

func (a *api) reconcileScannedSnapshot(scannedIDs []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(scannedIDs) == 0 {
		if _, err := tx.Exec(`DELETE FROM sender_emails`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM gmail_message_cache`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM senders`); err != nil {
			return err
		}
		return tx.Commit()
	}

	if _, err := tx.Exec(`
		DELETE FROM sender_emails
		WHERE gmail_message_id <> ALL($1)
	`, pq.Array(scannedIDs)); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		DELETE FROM gmail_message_cache
		WHERE gmail_message_id <> ALL($1)
	`, pq.Array(scannedIDs)); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		DELETE FROM senders s
		WHERE NOT EXISTS (
			SELECT 1 FROM sender_emails e WHERE e.sender_id = s.id
		)
	`); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *api) storeGmailMessage(msg *gmail.Message) (bool, error) {
	fromHeader := headerValue(msg.Payload.Headers, "From")
	subject := headerValue(msg.Payload.Headers, "Subject")
	receivedAt := parseGmailDate(headerValue(msg.Payload.Headers, "Date"))
	displayName, senderEmail := parseFromHeader(fromHeader)
	if senderEmail == "" {
		return false, nil
	}

	bodyText := extractPlainBody(msg.Payload)
	labelIDs := strings.Join(msg.LabelIds, ",")

	tx, err := a.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var senderID int
	err = tx.QueryRow(`
		INSERT INTO senders (email, display_name)
		VALUES ($1, $2)
		ON CONFLICT(email)
		DO UPDATE SET display_name = COALESCE(EXCLUDED.display_name, senders.display_name)
		RETURNING id
	`, senderEmail, displayName).Scan(&senderID)
	if err != nil {
		return false, err
	}

	result, err := tx.Exec(`
		INSERT INTO sender_emails (
			gmail_message_id,
			gmail_thread_id,
			sender_id,
			subject,
			snippet,
			body_text,
			received_at,
			label_ids
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(gmail_message_id) DO NOTHING
	`, msg.Id, msg.ThreadId, senderID, subject, msg.Snippet, bodyText, receivedAt, labelIDs)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (a *api) getInboxStats(w http.ResponseWriter, r *http.Request) {
	var stats inboxStats
	// Total scanned emails is best represented by the message-id cache, which
	// tracks every scanned id (not only rows that parsed into sender_emails).
	if err := a.db.QueryRow("SELECT COUNT(*) FROM gmail_message_cache").Scan(&stats.TotalEmails); err != nil {
		http.Error(w, `{"error":"failed querying email count"}`, http.StatusInternalServerError)
		return
	}
	if err := a.db.QueryRow("SELECT COUNT(*) FROM senders").Scan(&stats.TotalSenders); err != nil {
		http.Error(w, `{"error":"failed querying sender count"}`, http.StatusInternalServerError)
		return
	}
	if err := a.db.QueryRow("SELECT COUNT(*) FROM gmail_accounts").Scan(&stats.ConnectedApps); err != nil {
		http.Error(w, `{"error":"failed querying account count"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(stats)
}

func (a *api) getSenders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`
		SELECT
			s.id,
			s.email,
			COALESCE(NULLIF(s.display_name, ''), s.email) AS display_name,
			COUNT(e.id) AS email_count,
			COUNT(DISTINCT e.gmail_thread_id) AS thread_count,
			MAX(e.received_at) AS last_received_at
		FROM senders s
		LEFT JOIN sender_emails e ON e.sender_id = s.id
		GROUP BY s.id, s.email, s.display_name
		ORDER BY email_count DESC, last_received_at DESC NULLS LAST
	`)
	if err != nil {
		http.Error(w, `{"error":"failed querying senders"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []senderSummary{}
	for rows.Next() {
		var item senderSummary
		if err := rows.Scan(&item.ID, &item.Email, &item.DisplayName, &item.EmailCount, &item.ThreadCount, &item.LastReceivedAt); err != nil {
			http.Error(w, `{"error":"failed reading sender rows"}`, http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	json.NewEncoder(w).Encode(items)
}

func (a *api) getSenderEmails(w http.ResponseWriter, r *http.Request) {
	senderID := mux.Vars(r)["senderId"]
	rows, err := a.db.Query(`
		SELECT
			id,
			gmail_message_id,
			COALESCE(gmail_thread_id, ''),
			COALESCE(subject, ''),
			COALESCE(snippet, ''),
			COALESCE(body_text, ''),
			received_at
		FROM sender_emails
		WHERE sender_id = $1
		ORDER BY received_at DESC NULLS LAST
	`, senderID)
	if err != nil {
		http.Error(w, `{"error":"failed querying sender emails"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []senderEmail{}
	for rows.Next() {
		var item senderEmail
		if err := rows.Scan(&item.ID, &item.GmailMessageID, &item.GmailThreadID, &item.Subject, &item.Snippet, &item.BodyText, &item.ReceivedAt); err != nil {
			http.Error(w, `{"error":"failed reading sender emails"}`, http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	json.NewEncoder(w).Encode(items)
}

type bulkEmailOpRequest struct {
	GmailMessageIds []string `json:"gmailMessageIds"`
}

func (a *api) bulkTrashEmails(w http.ResponseWriter, r *http.Request) {
	var req bulkEmailOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.GmailMessageIds) == 0 {
		http.Error(w, `{"error":"gmailMessageIds is required"}`, http.StatusBadRequest)
		return
	}

	accountEmail, token, err := a.loadPrimaryToken()
	if err != nil {
		http.Error(w, `{"error":"connect Gmail first"}`, http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	tokenSource := a.oauthConfig.TokenSource(ctx, token)
	service, err := gmail.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		http.Error(w, `{"error":"failed creating gmail service"}`, http.StatusBadGateway)
		return
	}

	start := time.Now()
	succeeded := make([]string, 0, len(req.GmailMessageIds))
	failed := 0
	warning := ""

	// Try batchModify first — one API call per 1000 IDs.
	const batchSize = 1000
	batchOK := true
	for i := 0; i < len(req.GmailMessageIds); i += batchSize {
		end := i + batchSize
		if end > len(req.GmailMessageIds) {
			end = len(req.GmailMessageIds)
		}
		chunk := req.GmailMessageIds[i:end]

		err := service.Users.Messages.BatchModify("me", &gmail.BatchModifyMessagesRequest{
			Ids:         chunk,
			AddLabelIds: []string{"TRASH"},
		}).Do()
		if err != nil {
			log.Printf("bulkTrashEmails: batchModify failed: %v", err)
			batchOK = false
			break
		}
		succeeded = append(succeeded, chunk...)
	}

	// If batch failed, fall back to highly-concurrent per-message Trash with retry.
	if !batchOK {
		warning = "batch_modify_fallback"
		succeeded = succeeded[:0]
		failed = 0

		jobCh := make(chan string, len(req.GmailMessageIds))
		for _, id := range req.GmailMessageIds {
			jobCh <- id
		}
		close(jobCh)

		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for id := range jobCh {
					_, trashErr := withGmailRetry(func() (*gmail.Message, error) {
						return service.Users.Messages.Trash("me", id).Do()
					})
					mu.Lock()
					if trashErr != nil {
						failed++
					} else {
						succeeded = append(succeeded, id)
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}

	if len(succeeded) > 0 {
		if _, err := a.db.Exec(`
			DELETE FROM sender_emails
			WHERE gmail_message_id = ANY($1)
		`, pq.Array(succeeded)); err != nil {
			log.Printf("bulkTrashEmails: failed deleting local rows: %v", err)
			warning = "local_db_cleanup_failed"
		}
		if _, err := a.db.Exec(`
			DELETE FROM gmail_message_cache
			WHERE gmail_message_id = ANY($1)
		`, pq.Array(succeeded)); err != nil {
			log.Printf("bulkTrashEmails: failed deleting cache rows: %v", err)
		}
	}

	elapsed := time.Since(start)
	json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"connectedAs":  accountEmail,
		"processed":    len(succeeded),
		"failedCount":  failed,
		"durationMs":   elapsed.Milliseconds(),
		"warning":      warning,
	})
}

func (a *api) bulkDeleteEmails(w http.ResponseWriter, r *http.Request) {
	var req bulkEmailOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.GmailMessageIds) == 0 {
		http.Error(w, `{"error":"gmailMessageIds is required"}`, http.StatusBadRequest)
		return
	}

	accountEmail, token, err := a.loadPrimaryToken()
	if err != nil {
		http.Error(w, `{"error":"connect Gmail first"}`, http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	tokenSource := a.oauthConfig.TokenSource(ctx, token)
	service, err := gmail.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		http.Error(w, `{"error":"failed creating gmail service"}`, http.StatusBadGateway)
		return
	}

	succeeded := make([]string, 0, len(req.GmailMessageIds))
	failed := 0

	for _, id := range req.GmailMessageIds {
		if err := service.Users.Messages.Delete("me", id).Do(); err != nil {
			failed++
			continue
		}
		succeeded = append(succeeded, id)
	}

	if len(succeeded) > 0 {
		if _, err := a.db.Exec(`
			DELETE FROM sender_emails
			WHERE gmail_message_id = ANY($1)
		`, pq.Array(succeeded)); err != nil {
			http.Error(w, `{"error":"failed deleting emails from local db"}`, http.StatusInternalServerError)
			return
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"connectedAs":  accountEmail,
		"processed":   len(succeeded),
		"failedCount": failed,
	})
}

func (a *api) loadPrimaryToken() (string, *oauth2.Token, error) {
	var email, accessToken string
	var refreshToken sql.NullString
	var expiry sql.NullTime
	err := a.db.QueryRow(`
		SELECT email, access_token, refresh_token, token_expiry
		FROM gmail_accounts
		ORDER BY updated_at DESC
		LIMIT 1
	`).Scan(&email, &accessToken, &refreshToken, &expiry)
	if err != nil {
		return "", nil, err
	}

	token := &oauth2.Token{
		AccessToken: accessToken,
	}
	if refreshToken.Valid {
		token.RefreshToken = refreshToken.String
	}
	if expiry.Valid {
		token.Expiry = expiry.Time
	}
	return email, token, nil
}

func (a *api) consumeState(state string) bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	expiry, ok := a.stateStore[state]
	if !ok {
		return false
	}
	delete(a.stateStore, state)
	return time.Now().Before(expiry)
}

func randomState() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("state-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(bytes)
}

func headerValue(headers []*gmail.MessagePartHeader, key string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, key) {
			return h.Value
		}
	}
	return ""
}

func parseGmailDate(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parsed, err := mail.ParseDate(v)
	if err != nil {
		return nil
	}
	return &parsed
}

var fromRegex = regexp.MustCompile(`(?i)^(.*)<([^>]+)>$`)

func parseFromHeader(from string) (string, string) {
	from = strings.TrimSpace(from)
	if from == "" {
		return "", ""
	}

	matches := fromRegex.FindStringSubmatch(from)
	if len(matches) == 3 {
		name := strings.Trim(strings.TrimSpace(matches[1]), `"`)
		email := strings.ToLower(strings.TrimSpace(matches[2]))
		return name, email
	}
	return "", strings.ToLower(from)
}

func extractPlainBody(payload *gmail.MessagePart) string {
	if payload == nil {
		return ""
	}
	if strings.HasPrefix(payload.MimeType, "text/plain") && payload.Body != nil && payload.Body.Data != "" {
		data := payload.Body.Data
		decoded, err := base64.RawURLEncoding.DecodeString(data)
		if err == nil {
			return string(decoded)
		}
	}
	for _, part := range payload.Parts {
		if body := extractPlainBody(part); body != "" {
			return body
		}
	}
	return ""
}

func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonContentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep callback human-readable when accessed in browser.
		if !strings.Contains(r.URL.Path, "/auth/google/callback") {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}
