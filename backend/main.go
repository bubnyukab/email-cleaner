package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
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
	Running     bool      `json:"running"`
	Scanned     int       `json:"scanned"`
	Total       int64     `json:"total"`
	Inserted    int       `json:"inserted"`
	Failed      int       `json:"failed"`
	ConnectedAs string    `json:"connectedAs,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
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
		Scopes:       []string{gmail.GmailReadonlyScope},
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

	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"email":   profile.EmailAddress,
		"message": "Gmail connected. Return to the app and click Sync Inbox.",
	})
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

	inserted := 0
	failed := 0
	fetched := 0
	pageToken := ""
	messageIDs := make(chan string, 2000)
	var workers sync.WaitGroup
	workerCount := runtime.NumCPU() * 2
	if workerCount < 4 {
		workerCount = 4
	}
	if workerCount > 16 {
		workerCount = 16
	}

	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range messageIDs {
				msg, err := service.Users.Messages.Get("me", id).Format("full").Do()
				if err != nil {
					a.bumpSyncProgress(0, 1, 0, 1)
					continue
				}
				if ok, err := a.storeGmailMessage(msg); err == nil && ok {
					a.bumpSyncProgress(0, 1, 1, 0)
					continue
				}
				a.bumpSyncProgress(0, 1, 0, 1)
			}
		}()
	}

	for {
		req := service.Users.Messages.List("me").Q("in:inbox").MaxResults(500)
		if pageToken != "" {
			req = req.PageToken(pageToken)
		}

		listResp, err := req.Do()
		if err != nil {
			a.setSyncError(err.Error())
			close(messageIDs)
			workers.Wait()
			http.Error(w, `{"error":"failed listing inbox messages"}`, http.StatusBadGateway)
			return
		}

		if listResp.ResultSizeEstimate > 0 {
			a.bumpSyncProgress(listResp.ResultSizeEstimate, 0, 0, 0)
		}
		fetched += len(listResp.Messages)
		for _, m := range listResp.Messages {
			messageIDs <- m.Id
		}

		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	close(messageIDs)
	workers.Wait()

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

func (a *api) bumpSyncProgress(total int64, scanned, inserted, failed int) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if total > a.syncStatus.Total {
		a.syncStatus.Total = total
	}
	a.syncStatus.Scanned += scanned
	a.syncStatus.Inserted += inserted
	a.syncStatus.Failed += failed
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
	if err := a.db.QueryRow("SELECT COUNT(*) FROM sender_emails").Scan(&stats.TotalEmails); err != nil {
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
		LIMIT 500
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
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC1123Z, v)
	if err != nil {
		t2, err2 := time.Parse(time.RFC1123, v)
		if err2 != nil {
			return nil
		}
		return &t2
	}
	return &t
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
