package main


import (
	"log"
	"net/http"


	"github.com/dulchik/email-cleaner/gmail"
	"github.com/dulchik/email-cleaner/server"
)


func main() {
	// Initialize Gmail client (uses credentials.json in working dir)
	gc, err := gmail.NewClientFromCredentials("credentials.json", "token.json")
	if err != nil {
		log.Fatalf("gmail client: %v", err)
	}


	// Load cache (creates cache/ if missing)
	cache, err := gmail.LoadCache("cache/emails.json")
	if err != nil {
		log.Fatalf("load cache: %v", err)
	}
	gc.SetCache(cache)


	// Start background fetcher (non-blocking)
	go gmail.StartBackgroundSync(gc)


	// Server binds handlers and serves UI
	h := server.NewServer(gc)
	log.Println("Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", h))
}
