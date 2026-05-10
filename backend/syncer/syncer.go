package syncer

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"api/gmailutil"
	"api/models"
	"api/store"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Syncer is the interface HTTP handlers use: start a sync, poll status.
type Syncer interface {
	Run(ctx context.Context, accountEmail string) error
	Status() models.SyncStatus
	IsRunning() bool
}

// GmailSyncer is the production implementation. It owns all sync state and
// all Gmail scan logic. Callers never touch raw DB or OAuth tokens directly.
type GmailSyncer struct {
	store       store.Store
	oauthConfig *oauth2.Config
	mu          sync.Mutex
	status      models.SyncStatus
}

func New(st store.Store, oauthConfig *oauth2.Config) *GmailSyncer {
	return &GmailSyncer{store: st, oauthConfig: oauthConfig}
}

func (s *GmailSyncer) Status() models.SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *GmailSyncer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Running
}

// Run synchronously scans all Gmail messages for accountEmail (empty = primary
// account) and stores them. Blocks until complete. Returns a non-nil error only
// for fatal failures; per-message failures are counted in Status().Failed.
func (s *GmailSyncer) Run(ctx context.Context, accountEmail string) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return fmt.Errorf("sync already running")
	}
	s.status = models.SyncStatus{Running: true, StartedAt: time.Now()}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.status.Running = false
		s.status.FinishedAt = time.Now()
		s.mu.Unlock()
	}()

	resolvedEmail, token, err := s.store.GetAccountToken(accountEmail)
	if err != nil {
		s.setError(err.Error())
		return fmt.Errorf("connect Gmail first via /api/go/auth/google/start")
	}
	s.setConnectedAs(resolvedEmail)

	syncAccountID, _ := s.store.ResolveAccountID(resolvedEmail)

	if err := s.store.BootstrapMessageCache(); err != nil {
		s.setError(err.Error())
		return fmt.Errorf("failed bootstrapping message cache")
	}

	tokenSource := s.oauthConfig.TokenSource(ctx, token)
	if currentToken, err := tokenSource.Token(); err == nil {
		_ = s.store.UpsertAccount(resolvedEmail, currentToken.AccessToken, currentToken.RefreshToken, currentToken.Expiry)
	}

	service, err := gmail.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		s.setError(err.Error())
		return fmt.Errorf("failed creating gmail service")
	}

	profile, err := gmailutil.WithRetry(func() (*gmail.Profile, error) {
		return service.Users.GetProfile("me").Do()
	})
	if err == nil && profile.MessagesTotal > 0 {
		s.mu.Lock()
		s.status.Total = int64(profile.MessagesTotal)
		s.mu.Unlock()
	}

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
					s.bump(0, 0, 0, 1, 0, 1)
					continue
				}

				data := parseMessage(msg, syncAccountID)
				ok, storeErr := s.store.StoreMessage(data)
				if storeErr != nil {
					s.bump(0, 0, 0, 1, 0, 1)
					continue
				}

				if err := s.store.MarkMessageCached(msg.Id); err != nil {
					s.bump(0, 0, 0, 1, 0, 1)
					continue
				}

				if ok {
					s.bump(0, 0, 0, 1, 1, 0)
				} else {
					s.bump(0, 0, 0, 1, 0, 0)
				}
			}
		}()
	}

	pageToken := ""
	var listErr error
	for {
		req := service.Users.Messages.List("me").MaxResults(500)
		if pageToken != "" {
			req = req.PageToken(pageToken)
		}

		listResp, err := gmailutil.WithRetry(func() (*gmail.ListMessagesResponse, error) {
			return req.Do()
		})
		if err != nil {
			s.setError(err.Error())
			listErr = fmt.Errorf("failed listing messages after retries")
			break
		}

		ids := make([]string, 0, len(listResp.Messages))
		for _, m := range listResp.Messages {
			ids = append(ids, m.Id)
		}
		allScannedIDs = append(allScannedIDs, ids...)

		if len(ids) > 0 {
			missing, err := s.store.FilterUncachedIDs(ids)
			if err != nil {
				s.setError(err.Error())
				listErr = fmt.Errorf("failed querying message cache")
				break
			}
			s.bump(0, int64(len(ids)), int64(len(missing)), 0, 0, 0)
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

	if listErr != nil {
		return listErr
	}

	if err := s.store.ReconcileSnapshot(allScannedIDs, syncAccountID); err != nil {
		s.setError(err.Error())
		return fmt.Errorf("failed reconciling scanned snapshot")
	}

	return nil
}

// RunScheduled runs a background ticker that triggers sync according to the
// user-configured sync_interval preference. Call once from main in a goroutine.
func (s *GmailSyncer) RunScheduled() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		interval := s.syncInterval()

		s.mu.Lock()
		if interval == 0 {
			s.status.NextSyncAt = nil
			s.mu.Unlock()
			continue
		}
		lastFinished := s.status.FinishedAt
		running := s.status.Running
		s.mu.Unlock()

		nextAt := lastFinished.Add(interval)
		s.mu.Lock()
		s.status.NextSyncAt = &nextAt
		s.mu.Unlock()

		if !running && !lastFinished.IsZero() && time.Now().After(nextAt) {
			log.Printf("scheduled sync: triggering automatic sync")
			go func() {
				if err := s.Run(context.Background(), ""); err != nil {
					log.Printf("scheduled sync error: %v", err)
				}
				s.mu.Lock()
				log.Printf("scheduled sync completed: inserted=%d failed=%d", s.status.Inserted, s.status.Failed)
				s.mu.Unlock()
			}()
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (s *GmailSyncer) bump(totalEst, checkedDelta, pendingDelta int64, scannedDelta, insertedDelta, failedDelta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if totalEst > s.status.Total {
		s.status.Total = totalEst
	}
	s.status.Checked += checkedDelta
	s.status.PendingTotal += pendingDelta
	s.status.Scanned += scannedDelta
	s.status.Inserted += insertedDelta
	s.status.Failed += failedDelta
}

func (s *GmailSyncer) setError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastError = msg
}

func (s *GmailSyncer) setConnectedAs(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.ConnectedAs = email
}

func (s *GmailSyncer) syncInterval() time.Duration {
	val, err := s.store.GetPreferenceValue("sync_interval")
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

func parseMessage(msg *gmail.Message, accountID int) models.MessageData {
	fromHeader := gmailutil.HeaderValue(msg.Payload.Headers, "From")
	displayName, senderEmail := gmailutil.ParseFromHeader(fromHeader)
	listUnsub := gmailutil.HeaderValue(msg.Payload.Headers, "List-Unsubscribe")
	unsubURL, unsubMailto := "", ""
	if listUnsub != "" {
		unsubURL, unsubMailto = gmailutil.ParseListUnsubscribe(listUnsub)
	}
	return models.MessageData{
		GmailMessageID:    msg.Id,
		GmailThreadID:     msg.ThreadId,
		SenderEmail:       senderEmail,
		DisplayName:       displayName,
		Subject:           gmailutil.HeaderValue(msg.Payload.Headers, "Subject"),
		Snippet:           msg.Snippet,
		BodyText:          gmailutil.ExtractPlainBody(msg.Payload),
		BodyHTML:          gmailutil.ExtractHTMLBody(msg.Payload),
		LabelIDs:          strings.Join(msg.LabelIds, ","),
		ListUnsubscribe:   listUnsub,
		UnsubscribeURL:    unsubURL,
		UnsubscribeMailto: unsubMailto,
		ReceivedAt:        gmailutil.ParseDate(gmailutil.HeaderValue(msg.Payload.Headers, "Date")),
		SizeEstimate:      msg.SizeEstimate,
		AccountID:         accountID,
	}
}
