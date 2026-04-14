package gmail

// Email is the view model used by the UI
type Email struct {
	ID          string   `json:"id"`
	From        string   `json:"from"`
	Subject     string   `json:"subject"`
	Unsubscribe string   `json:"unsubscribe"`
	Labels      []string `json:"labels"`
}

// Cache holds persisted emails
type Cache struct {
	Messages map[string]Email `json:"messages"`
}

func NewCache() *Cache {
	return &Cache{Messages: make(map[string]Email)}
}
