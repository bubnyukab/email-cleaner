// backend/main.go 
package main

import (
	"encoding/json"
	"net/http"
)

func emailsHandler(w http.ResponseWriter, r *http.Request) {
	data := map[string]string{
		"message": "Gmail API will go here",
	}
	json.NewEncoder(w).Encode(data)
}

func main() {
	http.HandleFunc("/emails", emailsHandler)

	println("Server running on :8080")
	http.ListenAndServe(":8080", nil)
}
