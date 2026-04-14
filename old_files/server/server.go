package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/dulchik/email-cleaner/gmail"
)

// NewServer returns an http.Handler that serves UI and API endpoints
func NewServer(gc *gmail.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		http.ServeFile(w, r, "ui/index.html")
	})

	mux.HandleFunc("/api/emails", func(w http.ResponseWriter, r *http.Request) {
		gc.Mu.RLock()
		defer gc.Mu.RUnlock()
		// prepare emails slice
		em := make([]gmail.Email, 0, len(gc.Cache.Messages))
		for _, v := range gc.Cache.Messages {
			em = append(em, v)
		}
		// filtering
		q := r.URL.Query().Get("filter")
		if q == "withUnsub" {
			tmp := em[:0]
			for _, e := range em {
				if strings.TrimSpace(e.Unsubscribe) != "" {
					tmp = append(tmp, e)
				}
			}
			em = tmp
		}
		// sorting
		sortBy := r.URL.Query().Get("sort")
		switch sortBy {
		case "fromAsc":
			sort.Slice(em, func(i,j int) bool { return em[i].From < em[j].From })
		case "fromDesc":
			sort.Slice(em, func(i,j int) bool { return em[i].From > em[j].From })
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"emails": em,
			"fetchedEmails": gmail.FetchedCount(),
		})
	})

	// Serve static UI folder
	http.Handle("/", http.FileServer(http.Dir("../ui")))

	return mux
}
