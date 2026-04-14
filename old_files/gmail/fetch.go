package gmail

import (
	"log"
	"sync"
)

var (
	emails []Email
	fetchedEmails int
)

// StartBackgroundSync runs a worker pool that fetches metadata for message ids that are not in cache.
func StartBackgroundSync(c *Client) {
	// list all ids
	ids, err := c.ListMessageIDs(nil)
	if err != nil {
		log.Printf("list ids: %v", err)
		return
	}
	// prepare set of cached ids to skip
	cached := make(map[string]struct{})
	c.Mu.RLock()
	for id := range c.Cache.Messages {
		cached[id] = struct{}{}
	}
	c.Mu.RUnlock()

	// worker pool
	workers := 10
	idCh := make(chan string, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range idCh {
				if _, ok := cached[id]; ok {
					continue
				}
				e, err := c.FetchMetadata(id)
				if err != nil {
					log.Printf("fetch meta %s: %v", id, err)
					continue
				}
				// write to cache and save
				c.Mu.Lock()
				c.Cache.Messages[id] = e
				// also append to global emails slice for server display
				emails = append(emails, e)
				fetchedEmails++
				c.Mu.Unlock()
			}
		}()
	}

	for _, id := range ids {
		idCh <- id
	}
	close(idCh)
	wg.Wait()
	// persist cache file
	_ = SaveCache("cache/emails.json", c.Cache)
}
