package gmail

import (
	"encoding/json"
	"os"
)

func LoadCache(path string) (*Cache, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		c := NewCache()
		_ = SaveCache(path, c)
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil {
		return NewCache(), nil
	}
	if c.Messages == nil {
		c.Messages = make(map[string]Email)
	}
	return &c, nil
}

func SaveCache(path string, c *Cache) error {
	_ = os.MkdirAll("cache", 0755)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
