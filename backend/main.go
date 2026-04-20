package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"api/gmailutil"
	"api/models"

	"github.com/gorilla/mux"
	"github.com/lib/pq"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"github.com/joho/godotenv"
)

// api is the central server struct. All HTTP handlers are methods on it.
// Type aliases make it easy to migrate to models package incrementally.
type (
	syncStatus            = models.SyncStatus
	inboxStats            = models.InboxStats
	senderSummary         = models.SenderSummary
	senderEmail           = models.SenderEmail
	paginatedSenderEmails = models.PaginatedSenderEmails
)

type api struct {
	db          *sql.DB
	oauthConfig *oauth2.Config
	stateStore  map[string]time.Time
	stateMu     sync.Mutex
	syncMu      sync.Mutex
	syncStatus  syncStatus
}

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
	router.HandleFunc("/api/go/senders/by-domain", app.getSendersByDomain).Methods("GET")
	router.HandleFunc("/api/go/senders/{senderId}/emails", app.getSenderEmails).Methods("GET")
	router.HandleFunc("/api/go/senders/{senderId}/unsubscribe", app.unsubscribeSender).Methods("POST")
	router.HandleFunc("/api/go/emails/bulk/trash", app.bulkTrashEmails).Methods("POST")
	router.HandleFunc("/api/go/emails/bulk/untrash", app.bulkUntrashEmails).Methods("POST")
	router.HandleFunc("/api/go/emails/bulk/delete", app.bulkDeleteEmails).Methods("POST")
	router.HandleFunc("/api/go/senders/bulk/trash", app.bulkTrashBySenders).Methods("POST")
	router.HandleFunc("/api/go/labels", app.getLabels).Methods("GET")
	router.HandleFunc("/api/go/senders/{senderId}/block", app.blockSender).Methods("POST")
	router.HandleFunc("/api/go/export/senders", app.exportSendersCSV).Methods("GET")
	router.HandleFunc("/api/go/analytics/top-senders", app.analyticsTopSenders).Methods("GET")
	router.HandleFunc("/api/go/analytics/timeline", app.analyticsTimeline).Methods("GET")
	router.HandleFunc("/api/go/analytics/labels", app.analyticsLabels).Methods("GET")
	router.HandleFunc("/api/go/accounts", app.getAccounts).Methods("GET")
	router.HandleFunc("/api/go/preferences", app.getPreferences).Methods("GET")
	router.HandleFunc("/api/go/preferences", app.putPreferences).Methods("PUT")

	// Start background scheduled sync watcher.
	go app.runScheduledSync()

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

	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	accountEmail, token, err := a.loadTokenForAccount(requestedAccount)
	if err != nil {
		a.setSyncError(err.Error())
		http.Error(w, `{"error":"connect Gmail first via /api/go/auth/google/start"}`, http.StatusBadRequest)
		return
	}
	a.setSyncConnectedAccount(accountEmail)

	syncAccountID, _ := a.resolveAccountID(accountEmail)

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
	profile, err := gmailutil.WithRetry(func() (*gmail.Profile, error) {
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
				msg, err := gmailutil.WithRetry(func() (*gmail.Message, error) {
					return service.Users.Messages.Get("me", id).Format("full").Do()
				})
				if err != nil {
					a.bumpSyncProgress(0, 0, 0, 1, 0, 1)
					continue
				}

				ok, storeErr := a.storeGmailMessage(msg, syncAccountID)
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

		listResp, err := gmailutil.WithRetry(func() (*gmail.ListMessagesResponse, error) {
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

	// Keep local counts aligned with the latest scanned snapshot for this account.
	if syncAccountID > 0 {
		if err := a.reconcileScannedSnapshot(allScannedIDs, syncAccountID); err != nil {
			a.setSyncError(err.Error())
			http.Error(w, `{"error":"failed reconciling scanned snapshot"}`, http.StatusBadGateway)
			return
		}
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

func (a *api) reconcileScannedSnapshot(scannedIDs []string, accountID int) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(scannedIDs) == 0 {
		if _, err := tx.Exec(`DELETE FROM sender_emails WHERE gmail_account_id = $1`, accountID); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			DELETE FROM senders s
			WHERE s.gmail_account_id = $1
			  AND NOT EXISTS (
				SELECT 1 FROM sender_emails e WHERE e.sender_id = s.id
			  )
		`, accountID); err != nil {
			return err
		}
		return tx.Commit()
	}

	if _, err := tx.Exec(`
		DELETE FROM sender_emails
		WHERE gmail_account_id = $2
		  AND gmail_message_id <> ALL($1)
	`, pq.Array(scannedIDs), accountID); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		DELETE FROM senders s
		WHERE s.gmail_account_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM sender_emails e WHERE e.sender_id = s.id
		  )
	`, accountID); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *api) reconcileLegacyScannedSnapshot(scannedIDs []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(scannedIDs) == 0 {
		if _, err := tx.Exec(`DELETE FROM sender_emails`); err != nil {
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
		DELETE FROM senders s
		WHERE NOT EXISTS (
			SELECT 1 FROM sender_emails e WHERE e.sender_id = s.id
		)
	`); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *api) storeGmailMessage(msg *gmail.Message, accountID ...int) (bool, error) {
	fromHeader := gmailutil.HeaderValue(msg.Payload.Headers, "From")
	subject := gmailutil.HeaderValue(msg.Payload.Headers, "Subject")
	receivedAt := gmailutil.ParseDate(gmailutil.HeaderValue(msg.Payload.Headers, "Date"))
	displayName, senderEmail := gmailutil.ParseFromHeader(fromHeader)
	if senderEmail == "" {
		return false, nil
	}

	bodyText := gmailutil.ExtractPlainBody(msg.Payload)
	bodyHTML := gmailutil.ExtractHTMLBody(msg.Payload)
	labelIDs := strings.Join(msg.LabelIds, ",")
	listUnsub := gmailutil.HeaderValue(msg.Payload.Headers, "List-Unsubscribe")

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

	// If this message has an unsubscribe header and the sender doesn't yet,
	// parse and store the URL/mailto for one-click unsubscribe.
	if listUnsub != "" {
		unsubURL, unsubMailto := gmailutil.ParseListUnsubscribe(listUnsub)
		if unsubURL != "" || unsubMailto != "" {
			_, _ = tx.Exec(`
				UPDATE senders
				SET unsubscribe_url = COALESCE(senders.unsubscribe_url, $1),
				    unsubscribe_mailto = COALESCE(senders.unsubscribe_mailto, $2)
				WHERE id = $3 AND (senders.unsubscribe_url IS NULL AND senders.unsubscribe_mailto IS NULL)
			`, nullIfEmpty(unsubURL), nullIfEmpty(unsubMailto), senderID)
		}
	}

	var acctID interface{}
	if len(accountID) > 0 && accountID[0] > 0 {
		acctID = accountID[0]
	}

	// Also stamp the sender row with this account ID if not already set.
	if acctID != nil {
		_, _ = tx.Exec(`UPDATE senders SET gmail_account_id = $1 WHERE id = $2 AND gmail_account_id IS NULL`, acctID, senderID)
	}

	result, err := tx.Exec(`
		INSERT INTO sender_emails (
			gmail_message_id,
			gmail_thread_id,
			sender_id,
			subject,
			snippet,
			body_text,
			body_html,
			received_at,
			label_ids,
			size_bytes,
			list_unsubscribe,
			gmail_account_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT(gmail_message_id) DO NOTHING
	`, msg.Id, msg.ThreadId, senderID, subject, msg.Snippet, bodyText, nullIfEmpty(bodyHTML), receivedAt, labelIDs, msg.SizeEstimate, nullIfEmpty(listUnsub), acctID)
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
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	var stats inboxStats

	// When an account filter is active, scope counts to that account.
	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM sender_emails WHERE gmail_account_id = $1`, accountID).Scan(&stats.TotalEmails); err != nil {
			http.Error(w, `{"error":"failed querying email count"}`, http.StatusInternalServerError)
			return
		}
		if err := a.db.QueryRow(`SELECT COUNT(DISTINCT sender_id) FROM sender_emails WHERE gmail_account_id = $1`, accountID).Scan(&stats.TotalSenders); err != nil {
			http.Error(w, `{"error":"failed querying sender count"}`, http.StatusInternalServerError)
			return
		}
		if err := a.db.QueryRow(`SELECT COALESCE(SUM(size_bytes), 0) FROM sender_emails WHERE gmail_account_id = $1`, accountID).Scan(&stats.TotalSizeBytes); err != nil {
			http.Error(w, `{"error":"failed querying total size"}`, http.StatusInternalServerError)
			return
		}
	} else {
		if err := a.db.QueryRow("SELECT COUNT(*) FROM gmail_message_cache").Scan(&stats.TotalEmails); err != nil {
			http.Error(w, `{"error":"failed querying email count"}`, http.StatusInternalServerError)
			return
		}
		if err := a.db.QueryRow("SELECT COUNT(*) FROM senders").Scan(&stats.TotalSenders); err != nil {
			http.Error(w, `{"error":"failed querying sender count"}`, http.StatusInternalServerError)
			return
		}
		if err := a.db.QueryRow("SELECT COALESCE(SUM(size_bytes), 0) FROM sender_emails").Scan(&stats.TotalSizeBytes); err != nil {
			http.Error(w, `{"error":"failed querying total size"}`, http.StatusInternalServerError)
			return
		}
	}

	if err := a.db.QueryRow("SELECT COUNT(*) FROM gmail_accounts").Scan(&stats.ConnectedApps); err != nil {
		http.Error(w, `{"error":"failed querying account count"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(stats)
}

func (a *api) getSenders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	sortCol := q.Get("sort")
	sortOrder := strings.ToUpper(q.Get("order"))
	labelsParam := strings.TrimSpace(q.Get("labels"))
	accountEmail := strings.TrimSpace(q.Get("account"))

	// Allowlist sortable columns to prevent SQL injection.
	validSortCols := map[string]string{
		"email_count":      "email_count",
		"thread_count":     "thread_count",
		"display_name":     "display_name",
		"last_received":    "last_received_at",
		"total_size_bytes": "total_size_bytes",
	}
	orderByCol, ok := validSortCols[sortCol]
	if !ok {
		orderByCol = "email_count"
	}
	if sortOrder != "ASC" {
		sortOrder = "DESC"
	}

	// Build WHERE conditions.
	args := []any{}
	conditions := []string{}
	accountArgIdx := 0

	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err == nil {
			args = append(args, accountID)
			accountArgIdx = len(args)
			conditions = append(conditions, fmt.Sprintf(`EXISTS (
				SELECT 1 FROM sender_emails se_acc
				WHERE se_acc.sender_id = s.id
				  AND se_acc.gmail_account_id = $%d
			)`, accountArgIdx))
		}
	}

	if search != "" {
		args = append(args, "%"+search+"%")
		conditions = append(conditions, fmt.Sprintf("(s.email ILIKE $%d OR s.display_name ILIKE $%d)", len(args), len(args)))
	}

	if labelsParam != "" {
		labelList := strings.Split(labelsParam, ",")
		for i, lbl := range labelList {
			labelList[i] = strings.TrimSpace(lbl)
		}
		// Filter to senders that have at least one email with any of the requested labels.
		args = append(args, pq.Array(labelList))
		labelArgIdx := len(args)
		labelCond := fmt.Sprintf(`EXISTS (
			SELECT 1 FROM sender_emails se2
			WHERE se2.sender_id = s.id
			  AND string_to_array(se2.label_ids, ',') && $%d`, labelArgIdx)
		if accountArgIdx > 0 {
			labelCond += fmt.Sprintf(`
			  AND se2.gmail_account_id = $%d`, accountArgIdx)
		}
		labelCond += `
		)`
		conditions = append(conditions, labelCond)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Secondary sort: always add last_received_at DESC NULLS LAST as tiebreaker.
	secondarySort := ""
	if orderByCol != "last_received_at" {
		secondarySort = ", last_received_at DESC NULLS LAST"
	}

	joinClause := "LEFT JOIN sender_emails e ON e.sender_id = s.id"
	if accountArgIdx > 0 {
		joinClause += fmt.Sprintf(" AND e.gmail_account_id = $%d", accountArgIdx)
	}

	query := fmt.Sprintf(`
		SELECT
			s.id,
			s.email,
			COALESCE(NULLIF(s.display_name, ''), s.email) AS display_name,
			COALESCE(NULLIF(SPLIT_PART(s.email, '@', 2), ''), s.email) AS domain,
			COUNT(e.id) AS email_count,
			COUNT(DISTINCT e.gmail_thread_id) AS thread_count,
			COALESCE(SUM(e.size_bytes), 0) AS total_size_bytes,
			(s.unsubscribe_url IS NOT NULL OR s.unsubscribe_mailto IS NOT NULL) AS can_unsubscribe,
			s.unsubscribed_at,
			s.blocked_at,
			MAX(e.received_at) AS last_received_at,
			COALESCE(BOOL_OR(COALESCE(e.list_unsubscribe, '') <> ''), FALSE) AS has_list_unsubscribe,
			COALESCE(BOOL_OR('CATEGORY_PROMOTIONS' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_promotions,
			COALESCE(BOOL_OR('CATEGORY_SOCIAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_social,
			COALESCE(BOOL_OR('CATEGORY_UPDATES' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_updates,
			COALESCE(BOOL_OR('CATEGORY_PERSONAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_personal,
			COALESCE(BOOL_OR('INBOX' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_inbox
		FROM senders s
		%s
		%s
		GROUP BY s.id, s.email, s.display_name, s.unsubscribe_url, s.unsubscribe_mailto, s.unsubscribed_at, s.blocked_at
		ORDER BY %s %s NULLS LAST%s
	`, joinClause, whereClause, orderByCol, sortOrder, secondarySort)

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = a.db.Query(query, args...)
	} else {
		rows, err = a.db.Query(query)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying senders"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []senderSummary{}
	for rows.Next() {
		var item senderSummary
		var hasListUnsubscribe bool
		var hasPromotions bool
		var hasSocial bool
		var hasUpdates bool
		var hasPersonal bool
		if err := rows.Scan(
			&item.ID,
			&item.Email,
			&item.DisplayName,
			&item.Domain,
			&item.EmailCount,
			&item.ThreadCount,
			&item.TotalSizeBytes,
			&item.CanUnsubscribe,
			&item.UnsubscribedAt,
			&item.BlockedAt,
			&item.LastReceivedAt,
			&hasListUnsubscribe,
			&hasPromotions,
			&hasSocial,
			&hasUpdates,
			&hasPersonal,
			&item.HasInbox,
		); err != nil {
			http.Error(w, `{"error":"failed reading sender rows"}`, http.StatusInternalServerError)
			return
		}
		item.Category = classifySenderCategory(item.Email, hasListUnsubscribe, hasPromotions, hasSocial, hasUpdates, hasPersonal, item.HasInbox)
		item.KeepScore = scoreSender(item.Category, item.HasInbox)
		items = append(items, item)
	}
	json.NewEncoder(w).Encode(items)
}

func (a *api) getLabels(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	conditions := []string{"label_ids IS NOT NULL", "label_ids != ''"}
	args := []any{}
	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err == nil {
			args = append(args, accountID)
			conditions = append(conditions, fmt.Sprintf("gmail_account_id = $%d", len(args)))
		}
	}
	whereClause := strings.Join(conditions, " AND ")
	query := fmt.Sprintf(`
		SELECT DISTINCT unnest(string_to_array(label_ids, ',')) AS label
		FROM sender_emails
		WHERE %s
		ORDER BY label
	`, whereClause)
	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = a.db.Query(query, args...)
	} else {
		rows, err = a.db.Query(query)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying labels"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	labels := []string{}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			http.Error(w, `{"error":"failed reading labels"}`, http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(label) != "" {
			labels = append(labels, label)
		}
	}
	json.NewEncoder(w).Encode(labels)
}

func (a *api) getSendersByDomain(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	conditions := []string{}
	args := []any{}
	accountArgIdx := 0
	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err == nil {
			args = append(args, accountID)
			accountArgIdx = len(args)
			conditions = append(conditions, fmt.Sprintf(`EXISTS (
				SELECT 1 FROM sender_emails se_acc
				WHERE se_acc.sender_id = s.id
				  AND se_acc.gmail_account_id = $%d
			)`, accountArgIdx))
		}
	}
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	joinClause := "LEFT JOIN sender_emails e ON e.sender_id = s.id"
	if accountArgIdx > 0 {
		joinClause += fmt.Sprintf(" AND e.gmail_account_id = $%d", accountArgIdx)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(SPLIT_PART(s.email, '@', 2), ''), s.email) AS domain,
			COUNT(DISTINCT s.id) AS sender_count,
			COUNT(e.id) AS email_count,
			COALESCE(SUM(e.size_bytes), 0) AS total_size_bytes,
			MAX(e.received_at) AS last_received_at,
			COALESCE(BOOL_OR(COALESCE(e.list_unsubscribe, '') <> ''), FALSE) AS has_list_unsubscribe,
			COALESCE(BOOL_OR('CATEGORY_PROMOTIONS' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_promotions,
			COALESCE(BOOL_OR('CATEGORY_SOCIAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_social,
			COALESCE(BOOL_OR('CATEGORY_UPDATES' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_updates,
			COALESCE(BOOL_OR('CATEGORY_PERSONAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_personal,
			COALESCE(BOOL_OR('INBOX' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_inbox,
			ARRAY_REMOVE(ARRAY_AGG(DISTINCT s.email), NULL) AS sender_emails
		FROM senders s
		%s
		%s
		GROUP BY domain
		ORDER BY email_count DESC, domain ASC
	`, joinClause, whereClause)
	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = a.db.Query(query, args...)
	} else {
		rows, err = a.db.Query(query)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying sender domains"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type senderDomainSummary struct {
		Domain         string     `json:"domain"`
		SenderCount    int        `json:"senderCount"`
		EmailCount     int        `json:"emailCount"`
		TotalSizeBytes int64      `json:"totalSizeBytes"`
		LastReceivedAt *time.Time `json:"lastReceivedAt"`
		HasInbox       bool       `json:"hasInbox"`
		Category       string     `json:"category"`
		KeepScore      int        `json:"keepScore"`
		SenderEmails   []string   `json:"senderEmails"`
	}

	items := []senderDomainSummary{}
	for rows.Next() {
		var item senderDomainSummary
		var hasListUnsubscribe bool
		var hasPromotions bool
		var hasSocial bool
		var hasUpdates bool
		var hasPersonal bool
		var senderEmails pq.StringArray
		if err := rows.Scan(
			&item.Domain,
			&item.SenderCount,
			&item.EmailCount,
			&item.TotalSizeBytes,
			&item.LastReceivedAt,
			&hasListUnsubscribe,
			&hasPromotions,
			&hasSocial,
			&hasUpdates,
			&hasPersonal,
			&item.HasInbox,
			&senderEmails,
		); err != nil {
			http.Error(w, `{"error":"failed reading sender domain rows"}`, http.StatusInternalServerError)
			return
		}
		item.Category = classifySenderCategory(item.Domain, hasListUnsubscribe, hasPromotions, hasSocial, hasUpdates, hasPersonal, item.HasInbox)
		item.KeepScore = scoreSender(item.Category, item.HasInbox)
		item.SenderEmails = []string(senderEmails)
		items = append(items, item)
	}
	json.NewEncoder(w).Encode(items)
}

func (a *api) getSenderEmails(w http.ResponseWriter, r *http.Request) {
	senderID := mux.Vars(r)["senderId"]
	q := r.URL.Query()
	accountEmail := strings.TrimSpace(q.Get("account"))
	var accountID int
	hasAccountScope := false
	if accountEmail != "" {
		resolvedID, err := a.resolveAccountID(accountEmail)
		if err == nil {
			accountID = resolvedID
			hasAccountScope = true
		}
	}

	page := 1
	limit := 0 // 0 = no limit (backwards-compatible)
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = l
	}

	// Total count for pagination metadata.
	var total int
	countQuery := `SELECT COUNT(*) FROM sender_emails WHERE sender_id = $1`
	countArgs := []any{senderID}
	if hasAccountScope {
		countQuery += ` AND gmail_account_id = $2`
		countArgs = append(countArgs, accountID)
	}
	if err := a.db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		http.Error(w, `{"error":"failed counting sender emails"}`, http.StatusInternalServerError)
		return
	}

	// Build query with optional LIMIT/OFFSET.
	baseQuery := `
		SELECT
			id,
			gmail_message_id,
			COALESCE(gmail_thread_id, ''),
			COALESCE(subject, ''),
			COALESCE(snippet, ''),
			COALESCE(body_text, ''),
			COALESCE(body_html, ''),
			received_at,
			COALESCE(label_ids, '')
		FROM sender_emails
		WHERE sender_id = $1
		ORDER BY received_at DESC NULLS LAST
	`
	var rows *sql.Rows
	var err error
	queryArgs := []any{senderID}
	if hasAccountScope {
		queryArgs = append(queryArgs, accountID)
		baseQuery = strings.Replace(baseQuery, "WHERE sender_id = $1", "WHERE sender_id = $1 AND gmail_account_id = $2", 1)
	}
	if limit > 0 {
		offset := (page - 1) * limit
		if hasAccountScope {
			rows, err = a.db.Query(baseQuery+` LIMIT $3 OFFSET $4`, senderID, accountID, limit, offset)
		} else {
			rows, err = a.db.Query(baseQuery+` LIMIT $2 OFFSET $3`, senderID, limit, offset)
		}
	} else {
		rows, err = a.db.Query(baseQuery, queryArgs...)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying sender emails"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []senderEmail{}
	for rows.Next() {
		var item senderEmail
		if err := rows.Scan(&item.ID, &item.GmailMessageID, &item.GmailThreadID, &item.Subject, &item.Snippet, &item.BodyText, &item.BodyHTML, &item.ReceivedAt, &item.LabelIDs); err != nil {
			http.Error(w, `{"error":"failed reading sender emails"}`, http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}

	// Return paginated wrapper if page/limit were requested, plain array otherwise.
	if limit > 0 {
		json.NewEncoder(w).Encode(paginatedSenderEmails{
			Data:  items,
			Total: total,
			Page:  page,
			Limit: limit,
		})
	} else {
		json.NewEncoder(w).Encode(items)
	}
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

	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	accountEmail, token, err := a.loadTokenForAccount(requestedAccount)
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
					_, trashErr := gmailutil.WithRetry(func() (*gmail.Message, error) {
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

	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	accountEmail, token, err := a.loadTokenForAccount(requestedAccount)
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


func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func classifySenderCategory(email string, hasListUnsubscribe bool, hasPromotions bool, hasSocial bool, hasUpdates bool, hasPersonal bool, hasInbox bool) string {
	lower := strings.ToLower(email)
	isNoReply := strings.Contains(lower, "noreply") ||
		strings.Contains(lower, "no-reply") ||
		strings.Contains(lower, "donotreply") ||
		strings.Contains(lower, "notifications") ||
		strings.Contains(lower, "alerts")

	switch {
	case hasListUnsubscribe:
		return "Newsletter"
	case hasPromotions:
		return "Promotional"
	case hasSocial:
		return "Social"
	case hasUpdates:
		return "Notification"
	case isNoReply:
		return "No-reply"
	case hasPersonal || hasInbox:
		return "Personal"
	default:
		return "Other"
	}
}

func scoreSender(category string, hasInbox bool) int {
	score := 50
	if hasInbox {
		score += 20
	}
	switch category {
	case "Personal":
		score += 30
	case "Newsletter":
		score -= 30
	case "No-reply":
		score -= 20
	case "Promotional":
		score -= 20
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func (a *api) unsubscribeSender(w http.ResponseWriter, r *http.Request) {
	senderID := mux.Vars(r)["senderId"]
	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))

	var unsubURL, unsubMailto sql.NullString
	var unsubscribedAt sql.NullTime
	err := a.db.QueryRow(`
		SELECT unsubscribe_url, unsubscribe_mailto, unsubscribed_at
		FROM senders WHERE id = $1
	`, senderID).Scan(&unsubURL, &unsubMailto, &unsubscribedAt)
	if err != nil {
		http.Error(w, `{"error":"sender not found"}`, http.StatusNotFound)
		return
	}

	if unsubscribedAt.Valid {
		json.NewEncoder(w).Encode(map[string]any{
			"success":        true,
			"alreadyDone":    true,
			"unsubscribedAt": unsubscribedAt.Time,
		})
		return
	}

	if !unsubURL.Valid && !unsubMailto.Valid {
		http.Error(w, `{"error":"no unsubscribe link found for this sender"}`, http.StatusNotFound)
		return
	}

	// Prefer the HTTP URL (supports one-click); fall back to mailto.
	if unsubURL.Valid && unsubURL.String != "" {
		listUnsubPost := "List-Unsubscribe=One-Click"
		req, err := http.NewRequest("POST", unsubURL.String, strings.NewReader(listUnsubPost))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				// Any 2xx is a success; some return 200, some 204.
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					a.db.Exec(`UPDATE senders SET unsubscribed_at = NOW() WHERE id = $1`, senderID)
					json.NewEncoder(w).Encode(map[string]any{"success": true, "method": "http"})
					return
				}
			}
		}
		// If one-click POST failed, try plain GET.
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(unsubURL.String)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				a.db.Exec(`UPDATE senders SET unsubscribed_at = NOW() WHERE id = $1`, senderID)
				json.NewEncoder(w).Encode(map[string]any{"success": true, "method": "http_get"})
				return
			}
		}
	}

	// If we have a mailto link, send an unsubscribe email via Gmail API.
	if unsubMailto.Valid && unsubMailto.String != "" {
		mailto := unsubMailto.String
		if strings.HasPrefix(strings.ToLower(mailto), "mailto:") {
			mailto = mailto[7:]
		}

		_, token, err := a.loadTokenForAccount(requestedAccount)
		if err != nil {
			http.Error(w, `{"error":"connect Gmail first"}`, http.StatusBadRequest)
			return
		}

		ctx := context.Background()
		service, err := gmail.NewService(ctx, option.WithTokenSource(a.oauthConfig.TokenSource(ctx, token)))
		if err != nil {
			http.Error(w, `{"error":"failed creating gmail service"}`, http.StatusBadGateway)
			return
		}

		raw := fmt.Sprintf("To: %s\r\nSubject: Unsubscribe\r\nContent-Type: text/plain\r\n\r\nUnsubscribe", mailto)
		encoded := base64.URLEncoding.EncodeToString([]byte(raw))
		_, err = service.Users.Messages.Send("me", &gmail.Message{Raw: encoded}).Do()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed sending unsubscribe email: %s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		a.db.Exec(`UPDATE senders SET unsubscribed_at = NOW() WHERE id = $1`, senderID)
		json.NewEncoder(w).Encode(map[string]any{"success": true, "method": "mailto"})
		return
	}

	http.Error(w, `{"error":"unsubscribe failed"}`, http.StatusBadGateway)
}

type bulkSenderOpRequest struct {
	SenderIds []int `json:"senderIds"`
}

func (a *api) bulkTrashBySenders(w http.ResponseWriter, r *http.Request) {
	var req bulkSenderOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.SenderIds) == 0 {
		http.Error(w, `{"error":"senderIds is required"}`, http.StatusBadRequest)
		return
	}

	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	accountID := 0
	hasAccountScope := false
	if requestedAccount != "" {
		if resolvedID, resolveErr := a.resolveAccountID(requestedAccount); resolveErr == nil {
			accountID = resolvedID
			hasAccountScope = true
		}
	}

	var rows *sql.Rows
	var err error
	if hasAccountScope {
		rows, err = a.db.Query(`
			SELECT gmail_message_id
			FROM sender_emails
			WHERE sender_id = ANY($1) AND gmail_account_id = $2
		`, pq.Array(req.SenderIds), accountID)
	} else {
		rows, err = a.db.Query(`
			SELECT gmail_message_id FROM sender_emails WHERE sender_id = ANY($1)
		`, pq.Array(req.SenderIds))
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying emails for senders"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var gmailIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			http.Error(w, `{"error":"failed reading email ids"}`, http.StatusInternalServerError)
			return
		}
		gmailIDs = append(gmailIDs, id)
	}

	if len(gmailIDs) == 0 {
		json.NewEncoder(w).Encode(map[string]any{
			"success":     true,
			"processed":   0,
			"failedCount": 0,
		})
		return
	}

	accountEmail, token, err := a.loadTokenForAccount(requestedAccount)
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
	succeeded := make([]string, 0, len(gmailIDs))
	failed := 0
	warning := ""

	const batchSize = 1000
	batchOK := true
	for i := 0; i < len(gmailIDs); i += batchSize {
		end := i + batchSize
		if end > len(gmailIDs) {
			end = len(gmailIDs)
		}
		chunk := gmailIDs[i:end]
		err := service.Users.Messages.BatchModify("me", &gmail.BatchModifyMessagesRequest{
			Ids:         chunk,
			AddLabelIds: []string{"TRASH"},
		}).Do()
		if err != nil {
			log.Printf("bulkTrashBySenders: batchModify failed: %v", err)
			batchOK = false
			break
		}
		succeeded = append(succeeded, chunk...)
	}

	if !batchOK {
		warning = "batch_modify_fallback"
		succeeded = succeeded[:0]
		failed = 0

		jobCh := make(chan string, len(gmailIDs))
		for _, id := range gmailIDs {
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
					_, trashErr := gmailutil.WithRetry(func() (*gmail.Message, error) {
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
		if _, err := a.db.Exec(`DELETE FROM sender_emails WHERE gmail_message_id = ANY($1)`, pq.Array(succeeded)); err != nil {
			log.Printf("bulkTrashBySenders: failed deleting local rows: %v", err)
			warning = "local_db_cleanup_failed"
		}
		if _, err := a.db.Exec(`DELETE FROM gmail_message_cache WHERE gmail_message_id = ANY($1)`, pq.Array(succeeded)); err != nil {
			log.Printf("bulkTrashBySenders: failed deleting cache rows: %v", err)
		}
	}

	elapsed := time.Since(start)
	json.NewEncoder(w).Encode(map[string]any{
		"success":        true,
		"connectedAs":    accountEmail,
		"processed":      len(succeeded),
		"failedCount":    failed,
		"durationMs":     elapsed.Milliseconds(),
		"warning":        warning,
		"gmailMessageIds": succeeded,
	})
}

func (a *api) bulkUntrashEmails(w http.ResponseWriter, r *http.Request) {
	var req bulkEmailOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if len(req.GmailMessageIds) == 0 {
		http.Error(w, `{"error":"gmailMessageIds is required"}`, http.StatusBadRequest)
		return
	}

	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	_, token, err := a.loadTokenForAccount(requestedAccount)
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
				_, untrashErr := gmailutil.WithRetry(func() (*gmail.Message, error) {
					return service.Users.Messages.Untrash("me", id).Do()
				})
				mu.Lock()
				if untrashErr != nil {
					failed++
				} else {
					succeeded = append(succeeded, id)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Re-insert into cache so the next sync sees them as existing.
	for _, id := range succeeded {
		_ = a.markGmailMessageCached(id)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"processed":   len(succeeded),
		"failedCount": failed,
	})
}

func (a *api) loadPrimaryToken() (string, *oauth2.Token, error) {
	return a.loadTokenForAccount("")
}

func (a *api) loadTokenForAccount(accountEmail string) (string, *oauth2.Token, error) {
	var email, accessToken string
	var refreshToken sql.NullString
	var expiry sql.NullTime

	var err error
	if accountEmail != "" {
		err = a.db.QueryRow(`
			SELECT email, access_token, refresh_token, token_expiry
			FROM gmail_accounts
			WHERE email = $1
		`, accountEmail).Scan(&email, &accessToken, &refreshToken, &expiry)
	} else {
		err = a.db.QueryRow(`
			SELECT email, access_token, refresh_token, token_expiry
			FROM gmail_accounts
			ORDER BY updated_at DESC
			LIMIT 1
		`).Scan(&email, &accessToken, &refreshToken, &expiry)
	}
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

// resolveAccountID returns the gmail_accounts.id for the given email (or the
// most-recently-updated account if email is empty).
func (a *api) resolveAccountID(accountEmail string) (int, error) {
	var id int
	var err error
	if accountEmail != "" {
		err = a.db.QueryRow(`SELECT id FROM gmail_accounts WHERE email = $1`, accountEmail).Scan(&id)
	} else {
		err = a.db.QueryRow(`SELECT id FROM gmail_accounts ORDER BY updated_at DESC LIMIT 1`).Scan(&id)
	}
	return id, err
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
		if !strings.Contains(r.URL.Path, "/auth/google/callback") &&
			!strings.Contains(r.URL.Path, "/export/") {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}

// ── Analytics handlers ────────────────────────────────────────────────────────

func (a *api) analyticsTopSenders(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	args := []any{}
	whereClause := ""
	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
		args = append(args, accountID)
		whereClause = fmt.Sprintf("WHERE e.gmail_account_id = $%d", len(args))
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(s.display_name, ''), s.email) AS display_name,
			COUNT(e.id) AS email_count
		FROM senders s
		JOIN sender_emails e ON e.sender_id = s.id
		%s
		GROUP BY s.id, s.email, s.display_name
		ORDER BY email_count DESC
		LIMIT 10
	`, whereClause)

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = a.db.Query(query, args...)
	} else {
		rows, err = a.db.Query(query)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying top senders"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	result := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.Name, &it.Count); err != nil {
			http.Error(w, `{"error":"failed reading top senders"}`, http.StatusInternalServerError)
			return
		}
		result = append(result, it)
	}
	json.NewEncoder(w).Encode(result)
}

func (a *api) analyticsTimeline(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	args := []any{}
	conditions := []string{"received_at >= NOW() - INTERVAL '180 days'"}
	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
		args = append(args, accountID)
		conditions = append(conditions, fmt.Sprintf("gmail_account_id = $%d", len(args)))
	}
	query := fmt.Sprintf(`
		SELECT
			date_trunc('day', received_at)::date AS day,
			COUNT(*) AS count
		FROM sender_emails
		WHERE %s
		GROUP BY day
		ORDER BY day
	`, strings.Join(conditions, " AND "))

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = a.db.Query(query, args...)
	} else {
		rows, err = a.db.Query(query)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying timeline"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		Day   string `json:"day"`
		Count int    `json:"count"`
	}
	result := []item{}
	for rows.Next() {
		var it item
		var day time.Time
		if err := rows.Scan(&day, &it.Count); err != nil {
			http.Error(w, `{"error":"failed reading timeline"}`, http.StatusInternalServerError)
			return
		}
		it.Day = day.Format("2006-01-02")
		result = append(result, it)
	}
	json.NewEncoder(w).Encode(result)
}

func (a *api) analyticsLabels(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	args := []any{}
	conditions := []string{"label_ids IS NOT NULL", "label_ids != ''"}
	if accountEmail != "" {
		accountID, err := a.resolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
		args = append(args, accountID)
		conditions = append(conditions, fmt.Sprintf("gmail_account_id = $%d", len(args)))
	}
	query := fmt.Sprintf(`
		SELECT
			unnest(string_to_array(label_ids, ',')) AS label,
			COUNT(*) AS count
		FROM sender_emails
		WHERE %s
		GROUP BY label
		ORDER BY count DESC
	`, strings.Join(conditions, " AND "))

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = a.db.Query(query, args...)
	} else {
		rows, err = a.db.Query(query)
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying label analytics"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		Label string `json:"label"`
		Count int    `json:"count"`
	}
	result := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.Label, &it.Count); err != nil {
			http.Error(w, `{"error":"failed reading label analytics"}`, http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(it.Label) != "" {
			result = append(result, it)
		}
	}
	json.NewEncoder(w).Encode(result)
}

// ── Export handler ────────────────────────────────────────────────────────────

func (a *api) exportSendersCSV(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`
		SELECT
			s.email,
			COALESCE(NULLIF(s.display_name, ''), s.email),
			COUNT(e.id),
			COUNT(DISTINCT e.gmail_thread_id),
			COALESCE(SUM(e.size_bytes), 0),
			MAX(e.received_at),
			s.unsubscribed_at
		FROM senders s
		LEFT JOIN sender_emails e ON e.sender_id = s.id
		GROUP BY s.id, s.email, s.display_name, s.unsubscribed_at
		ORDER BY COUNT(e.id) DESC
	`)
	if err != nil {
		http.Error(w, `{"error":"failed querying senders"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"senders.csv\"")

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"email", "display_name", "email_count", "thread_count", "total_size_bytes", "last_received_at", "unsubscribed_at"})

	for rows.Next() {
		var (
			email, displayName    string
			emailCount, threadCount int
			totalSizeBytes        int64
			lastReceived          sql.NullTime
			unsubscribedAt        sql.NullTime
		)
		if err := rows.Scan(&email, &displayName, &emailCount, &threadCount, &totalSizeBytes, &lastReceived, &unsubscribedAt); err != nil {
			continue
		}
		lastReceivedStr := ""
		if lastReceived.Valid {
			lastReceivedStr = lastReceived.Time.Format(time.RFC3339)
		}
		unsubStr := ""
		if unsubscribedAt.Valid {
			unsubStr = unsubscribedAt.Time.Format(time.RFC3339)
		}
		_ = cw.Write([]string{
			email,
			displayName,
			strconv.Itoa(emailCount),
			strconv.Itoa(threadCount),
			strconv.FormatInt(totalSizeBytes, 10),
			lastReceivedStr,
			unsubStr,
		})
	}
	cw.Flush()
}

// ── Block sender ──────────────────────────────────────────────────────────────

func (a *api) blockSender(w http.ResponseWriter, r *http.Request) {
	senderID := mux.Vars(r)["senderId"]
	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))

	var senderEmail string
	var blockedAt sql.NullTime
	err := a.db.QueryRow(`SELECT email, blocked_at FROM senders WHERE id = $1`, senderID).
		Scan(&senderEmail, &blockedAt)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"sender not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"failed querying sender"}`, http.StatusInternalServerError)
		return
	}

	if blockedAt.Valid {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "alreadyDone": true})
		return
	}

	_, token, err := a.loadTokenForAccount(requestedAccount)
	if err != nil {
		http.Error(w, `{"error":"connect Gmail first"}`, http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	service, err := gmail.NewService(ctx, option.WithTokenSource(a.oauthConfig.TokenSource(ctx, token)))
	if err != nil {
		http.Error(w, `{"error":"failed creating gmail service"}`, http.StatusBadGateway)
		return
	}

	filter := &gmail.Filter{
		Criteria: &gmail.FilterCriteria{
			From: senderEmail,
		},
		Action: &gmail.FilterAction{
			RemoveLabelIds: []string{"INBOX"},
			AddLabelIds:    []string{"TRASH"},
		},
	}
	_, err = service.Users.Settings.Filters.Create("me", filter).Do()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed creating Gmail filter: %s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	_, _ = a.db.Exec(`UPDATE senders SET blocked_at = NOW() WHERE id = $1`, senderID)
	json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// ── Accounts handler ──────────────────────────────────────────────────────────

func (a *api) getAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT id, email, updated_at FROM gmail_accounts ORDER BY updated_at DESC`)
	if err != nil {
		http.Error(w, `{"error":"failed querying accounts"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type account struct {
		ID        int       `json:"id"`
		Email     string    `json:"email"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	result := []account{}
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.ID, &a.Email, &a.UpdatedAt); err != nil {
			continue
		}
		result = append(result, a)
	}
	json.NewEncoder(w).Encode(result)
}

// ── Preferences handlers ──────────────────────────────────────────────────────

func (a *api) getPreferences(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT key, value FROM user_preferences`)
	if err != nil {
		// Table might not exist yet; return empty object gracefully.
		json.NewEncoder(w).Encode(map[string]string{})
		return
	}
	defer rows.Close()

	result := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		result[k] = v
	}
	json.NewEncoder(w).Encode(result)
}

func (a *api) putPreferences(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	for k, v := range body {
		_, err := a.db.Exec(`
			INSERT INTO user_preferences (key, value, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
		`, k, v)
		if err != nil {
			http.Error(w, `{"error":"failed saving preference"}`, http.StatusInternalServerError)
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// sortStrings is used for deterministic output in analytics (unused import guard).
var _ = sort.Strings

// ── Scheduled sync ────────────────────────────────────────────────────────────

func (a *api) getSyncInterval() time.Duration {
	var val string
	err := a.db.QueryRow(`SELECT value FROM user_preferences WHERE key = 'sync_interval'`).Scan(&val)
	if err != nil {
		return 0
	}
	switch val {
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "24h", "daily":
		return 24 * time.Hour
	default:
		return 0
	}
}

func (a *api) runScheduledSync() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		interval := a.getSyncInterval()
		if interval == 0 {
			a.syncMu.Lock()
			a.syncStatus.NextSyncAt = nil
			a.syncMu.Unlock()
			continue
		}

		a.syncMu.Lock()
		lastFinished := a.syncStatus.FinishedAt
		running := a.syncStatus.Running
		a.syncMu.Unlock()

		nextAt := lastFinished.Add(interval)
		a.syncMu.Lock()
		a.syncStatus.NextSyncAt = &nextAt
		a.syncMu.Unlock()

		if !running && !lastFinished.IsZero() && time.Now().After(nextAt) {
			log.Printf("scheduled sync: triggering automatic sync")
			a.triggerSync()
		}
	}
}

func (a *api) triggerSync() {
	a.syncMu.Lock()
	if a.syncStatus.Running {
		a.syncMu.Unlock()
		return
	}
	a.syncStatus = syncStatus{
		Running:   true,
		StartedAt: time.Now(),
	}
	a.syncMu.Unlock()

	go func() {
		defer func() {
			a.syncMu.Lock()
			a.syncStatus.Running = false
			a.syncStatus.FinishedAt = time.Now()
			a.syncMu.Unlock()
		}()

		accountEmail, token, err := a.loadPrimaryToken()
		if err != nil {
			a.setSyncError(err.Error())
			return
		}
		a.setSyncConnectedAccount(accountEmail)
		syncAccountID, _ := a.resolveAccountID(accountEmail)

		_, _ = a.db.Exec(`
			INSERT INTO gmail_message_cache (gmail_message_id)
			SELECT gmail_message_id FROM sender_emails
			ON CONFLICT(gmail_message_id) DO NOTHING
		`)

		ctx := context.Background()
		tokenSource := a.oauthConfig.TokenSource(ctx, token)
		service, err := gmail.NewService(ctx, option.WithTokenSource(tokenSource))
		if err != nil {
			a.setSyncError(err.Error())
			return
		}

		profile, err := gmailutil.WithRetry(func() (*gmail.Profile, error) {
			return service.Users.GetProfile("me").Do()
		})
		if err == nil && profile.MessagesTotal > 0 {
			a.syncMu.Lock()
			a.syncStatus.Total = int64(profile.MessagesTotal)
			a.syncMu.Unlock()
		}

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
					msg, err := gmailutil.WithRetry(func() (*gmail.Message, error) {
						return service.Users.Messages.Get("me", id).Format("full").Do()
					})
					if err != nil {
						a.bumpSyncProgress(0, 0, 0, 1, 0, 1)
						continue
					}
					ok, storeErr := a.storeGmailMessage(msg, syncAccountID)
					if storeErr != nil {
						a.bumpSyncProgress(0, 0, 0, 1, 0, 1)
						continue
					}
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
			req := service.Users.Messages.List("me").MaxResults(500)
			if pageToken != "" {
				req = req.PageToken(pageToken)
			}
			listResp, err := gmailutil.WithRetry(func() (*gmail.ListMessagesResponse, error) {
				return req.Do()
			})
			if err != nil {
				a.setSyncError(err.Error())
				close(messageIDs)
				workers.Wait()
				return
			}

			ids := make([]string, 0, len(listResp.Messages))
			for _, m := range listResp.Messages {
				ids = append(ids, m.Id)
			}
			allScannedIDs = append(allScannedIDs, ids...)

			if len(ids) > 0 {
				rows, err := a.db.Query(`
					SELECT gmail_message_id FROM gmail_message_cache
					WHERE gmail_message_id = ANY($1)
				`, pq.Array(ids))
				if err != nil {
					a.setSyncError(err.Error())
					close(messageIDs)
					workers.Wait()
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

		if syncAccountID > 0 {
			if err := a.reconcileScannedSnapshot(allScannedIDs, syncAccountID); err != nil {
				a.setSyncError(err.Error())
			}
		} else {
			// Backwards compatibility fallback if account id cannot be resolved.
			if err := a.reconcileLegacyScannedSnapshot(allScannedIDs); err != nil {
				a.setSyncError(err.Error())
			}
		}
		log.Printf("scheduled sync completed: inserted=%d failed=%d", a.syncStatus.Inserted, a.syncStatus.Failed)
	}()
}
