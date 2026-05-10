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
	"strconv"
	"strings"
	"sync"
	"time"

	"api/gmailutil"
	"api/models"
	"api/store"
	"api/syncer"

	"github.com/gorilla/mux"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"github.com/joho/godotenv"
)

type api struct {
	store       store.Store
	syncer      syncer.Syncer
	oauthConfig *oauth2.Config
	stateStore  map[string]time.Time
	stateMu     sync.Mutex
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

	st := store.New(db)
	gs := syncer.New(st, oauthConfig)
	app := &api{
		store:       st,
		syncer:      gs,
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
	go gs.RunScheduled()

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

	if err := a.store.UpsertAccount(profile.EmailAddress, token.AccessToken, token.RefreshToken, token.Expiry); err != nil {
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
	if a.syncer.IsRunning() {
		http.Error(w, `{"error":"sync already running"}`, http.StatusConflict)
		return
	}

	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	if err := a.syncer.Run(r.Context(), requestedAccount); err != nil {
		msg := err.Error()
		if msg == "sync already running" {
			http.Error(w, `{"error":"sync already running"}`, http.StatusConflict)
			return
		}
		if strings.Contains(msg, "connect Gmail") {
			http.Error(w, `{"error":"connect Gmail first via /api/go/auth/google/start"}`, http.StatusBadRequest)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, msg), http.StatusBadGateway)
		return
	}

	status := a.syncer.Status()
	json.NewEncoder(w).Encode(map[string]any{
		"success":       true,
		"connectedAs":   status.ConnectedAs,
		"fetched":       status.Checked,
		"insertedCount": status.Inserted,
		"failedCount":   status.Failed,
	})
}

func (a *api) getSyncStatus(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(a.syncer.Status())
}

func (a *api) getInboxStats(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	var accountID int
	if accountEmail != "" {
		var err error
		accountID, err = a.store.ResolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
	}
	stats, err := a.store.GetInboxStats(accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying stats"}`, http.StatusInternalServerError)
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

	var accountID int
	if accountEmail != "" {
		accountID, _ = a.store.ResolveAccountID(accountEmail)
	}

	var labels []string
	if labelsParam != "" {
		for _, l := range strings.Split(labelsParam, ",") {
			if trimmed := strings.TrimSpace(l); trimmed != "" {
				labels = append(labels, trimmed)
			}
		}
	}

	items, err := a.store.GetSenders(models.GetSendersParams{
		AccountID: accountID,
		Search:    search,
		SortCol:   orderByCol,
		SortOrder: sortOrder,
		Labels:    labels,
	})
	if err != nil {
		http.Error(w, `{"error":"failed querying senders"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(items)
}

func (a *api) getLabels(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	var accountID int
	if accountEmail != "" {
		accountID, _ = a.store.ResolveAccountID(accountEmail)
	}
	labels, err := a.store.GetDistinctLabels(accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying labels"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(labels)
}

func (a *api) getSendersByDomain(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	var accountID int
	if accountEmail != "" {
		accountID, _ = a.store.ResolveAccountID(accountEmail)
	}
	items, err := a.store.GetSendersByDomain(accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying sender domains"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(items)
}

func (a *api) getSenderEmails(w http.ResponseWriter, r *http.Request) {
	senderID := mux.Vars(r)["senderId"]
	q := r.URL.Query()
	accountEmail := strings.TrimSpace(q.Get("account"))
	var accountID int
	if accountEmail != "" {
		accountID, _ = a.store.ResolveAccountID(accountEmail)
	}

	page := 1
	limit := 0 // 0 = no limit (backwards-compatible)
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = l
	}

	result, err := a.store.GetSenderEmails(senderID, accountID, page, limit)
	if err != nil {
		http.Error(w, `{"error":"failed querying sender emails"}`, http.StatusInternalServerError)
		return
	}

	if limit > 0 {
		json.NewEncoder(w).Encode(result)
	} else {
		json.NewEncoder(w).Encode(result.Data)
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
		if err := a.store.DeleteEmailsByMessageIDs(succeeded); err != nil {
			log.Printf("bulkTrashEmails: failed deleting local rows: %v", err)
			warning = "local_db_cleanup_failed"
		}
		if err := a.store.DeleteCacheByMessageIDs(succeeded); err != nil {
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
		if err := a.store.DeleteEmailsByMessageIDs(succeeded); err != nil {
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


func (a *api) unsubscribeSender(w http.ResponseWriter, r *http.Request) {
	senderID := mux.Vars(r)["senderId"]
	requestedAccount := strings.TrimSpace(r.URL.Query().Get("account"))

	unsubURL, unsubMailto, unsubscribedAt, err := a.store.GetSenderUnsubInfo(senderID)
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
					_ = a.store.MarkUnsubscribed(senderID)
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
				_ = a.store.MarkUnsubscribed(senderID)
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

		_ = a.store.MarkUnsubscribed(senderID)
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
	var accountID int
	if requestedAccount != "" {
		accountID, _ = a.store.ResolveAccountID(requestedAccount)
	}

	gmailIDs, err := a.store.GetEmailIDsBySenders(req.SenderIds, accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying emails for senders"}`, http.StatusInternalServerError)
		return
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
		if err := a.store.DeleteEmailsByMessageIDs(succeeded); err != nil {
			log.Printf("bulkTrashBySenders: failed deleting local rows: %v", err)
			warning = "local_db_cleanup_failed"
		}
		if err := a.store.DeleteCacheByMessageIDs(succeeded); err != nil {
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
		_ = a.store.MarkMessageCached(id)
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
	return a.store.GetAccountToken(accountEmail)
}

func (a *api) resolveAccountID(accountEmail string) (int, error) {
	return a.store.ResolveAccountID(accountEmail)
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
	var accountID int
	if accountEmail != "" {
		var err error
		accountID, err = a.store.ResolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
	}
	result, err := a.store.GetTopSenders(accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying top senders"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(result)
}

func (a *api) analyticsTimeline(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	var accountID int
	if accountEmail != "" {
		var err error
		accountID, err = a.store.ResolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
	}
	result, err := a.store.GetEmailTimeline(accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying timeline"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(result)
}

func (a *api) analyticsLabels(w http.ResponseWriter, r *http.Request) {
	accountEmail := strings.TrimSpace(r.URL.Query().Get("account"))
	var accountID int
	if accountEmail != "" {
		var err error
		accountID, err = a.store.ResolveAccountID(accountEmail)
		if err != nil {
			http.Error(w, `{"error":"account not found"}`, http.StatusNotFound)
			return
		}
	}
	result, err := a.store.GetLabelCounts(accountID)
	if err != nil {
		http.Error(w, `{"error":"failed querying label analytics"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(result)
}

// ── Export handler ────────────────────────────────────────────────────────────

func (a *api) exportSendersCSV(w http.ResponseWriter, r *http.Request) {
	exportRows, err := a.store.GetSendersForExport()
	if err != nil {
		http.Error(w, `{"error":"failed querying senders"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"senders.csv\"")

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"email", "display_name", "email_count", "thread_count", "total_size_bytes", "last_received_at", "unsubscribed_at"})

	for _, row := range exportRows {
		lastReceivedStr := ""
		if row.LastReceivedAt.Valid {
			lastReceivedStr = row.LastReceivedAt.Time.Format(time.RFC3339)
		}
		unsubStr := ""
		if row.UnsubscribedAt.Valid {
			unsubStr = row.UnsubscribedAt.Time.Format(time.RFC3339)
		}
		_ = cw.Write([]string{
			row.Email,
			row.DisplayName,
			strconv.Itoa(row.EmailCount),
			strconv.Itoa(row.ThreadCount),
			strconv.FormatInt(row.TotalSizeBytes, 10),
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

	senderEmail, blockedAt, err := a.store.GetSenderBlockInfo(senderID)
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

	_ = a.store.MarkBlocked(senderID)
	json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// ── Accounts handler ──────────────────────────────────────────────────────────

func (a *api) getAccounts(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.ListAccounts()
	if err != nil {
		http.Error(w, `{"error":"failed querying accounts"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(result)
}

// ── Preferences handlers ──────────────────────────────────────────────────────

func (a *api) getPreferences(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.GetPreferences()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{})
		return
	}
	json.NewEncoder(w).Encode(result)
}

func (a *api) putPreferences(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if err := a.store.UpsertPreferences(body); err != nil {
		http.Error(w, `{"error":"failed saving preference"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"success": true})
}

