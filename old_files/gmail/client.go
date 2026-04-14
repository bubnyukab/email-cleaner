package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Client wraps gmail.Service and cache
type Client struct {
	svc   *gmail.Service
	Cache *Cache
	Mu    sync.RWMutex
	// keep messageIDs so we don't re-list constantly
	messageIDs []string
}

// NewClientFromCredentials creates Client using OAuth credentials file and token path.
func NewClientFromCredentials(credsPath, tokenPath string) (*Client, error) {
	b, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("read creds: %w", err)
	}
	config, err := google.ConfigFromJSON(b, gmail.GmailModifyScope)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// redirect URI must match your OAuth client (desktop/web)
	config.RedirectURL = "http://localhost:8081/"

	client := getClient(config, tokenPath)
	svc, err := gmail.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("new gmail service: %w", err)
	}
	return &Client{svc: svc, Cache: NewCache()}, nil
}

func (c *Client) SetCache(cache *Cache) {
	c.Mu.Lock()
	c.Cache = cache
	// populate messageIDs from cache so we can skip already-known ids during sync
	for id := range cache.Messages {
		c.messageIDs = append(c.messageIDs, id)
	}
	c.Mu.Unlock()
}

func (c *Client) ListMessageIDs(ctx context.Context) ([]string, error) {
	user := "me"
	var ids []string
	pageToken := ""
	for {
		req := c.svc.Users.Messages.List(user).LabelIds("INBOX").MaxResults(500)
		if pageToken != "" {
			req.PageToken(pageToken)
		}
		resp, err := req.Do()
		if err != nil {
			return nil, err
		}
		for _, m := range resp.Messages {
			ids = append(ids, m.Id)
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	c.Mu.Lock()
	c.messageIDs = ids
	c.Mu.Unlock()
	return ids, nil
}

// FetchMetadata fetches message metadata for a given id (not modifying cache). Caller must handle concurrency.
func (c *Client) FetchMetadata(id string) (Email, error) {
	user := "me"
	m, err := c.svc.Users.Messages.Get(user, id).Format("metadata").Do()
	if err != nil {
		return Email{}, err
	}
	var from, subject, unsub string
	for _, h := range m.Payload.Headers {
		switch h.Name {
		case "From":
			from = h.Value
		case "Subject":
			subject = h.Value
		case "List-Unsubscribe":
			unsub = h.Value
		}
	}
	e := Email{ID: id, From: from, Subject: subject, Unsubscribe: unsub, Labels: m.LabelIds}
	return e, nil
}

// helper: get oauth2 client using token cache
func getClient(config *oauth2.Config, tokenPath string) *http.Client {
	// loads token from file or runs web flow
	tok, err := tokenFromFile(tokenPath)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokenPath, tok)
	}
	return config.Client(context.Background(), tok)
}

// --- oauth helpers (tokenFromFile, saveToken, getTokenFromWeb) ---
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	_ = json.NewDecoder(f).Decode(tok)
	return tok, nil
}

func saveToken(path string, token *oauth2.Token) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	// lightweight local web server to receive the code
	codeCh := make(chan string)
	srv := &http.Server{Addr: ":8081"}
	http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		fmt.Fprintf(w, "Authorization successful! You can close this window.")
		codeCh <- code
		go srv.Shutdown(context.Background())
	})
	go func() {
		_ = srv.ListenAndServe()
	}()
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then paste the code if needed:\n%v\n", authURL)
	code := <-codeCh
	tok, err := config.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

func FetchedCount() int {
	return fetchedEmails
}
