package centrifuge

import "sync"

// dictionaryCache remembers the dictionary the server sent in the connect reply,
// so a reconnect costs an id rather than a transfer.
//
// One entry, the most recent. A dictionary belongs to a profile and changes only
// when the server publishes a new version, so a single entry matches on
// essentially every reconnect. The exception is the window of a rolling deploy,
// where old and new nodes disagree: a client hopping between them re-downloads
// once per hop. That is bounded by the length of the deploy and needs no
// eviction policy to get right.
//
// The entry is keyed by id, which is a hash of the content, so it can never be
// returned for a dictionary whose bytes differ.
type dictionaryCache struct {
	mu   sync.Mutex
	id   string
	dict []byte
}

func newDictionaryCache() *dictionaryCache { return &dictionaryCache{} }

func (c *dictionaryCache) get(id string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == "" || id != c.id {
		return nil, false
	}
	return c.dict, true
}

func (c *dictionaryCache) put(id string, dict []byte) {
	if id == "" || len(dict) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id, c.dict = id, dict
}

// id returns what this client holds, to advertise at connect.
func (c *dictionaryCache) advertise() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// forget drops the entry. It is called when a frame fails to decode while that
// dictionary is in use: without it a corrupted entry would be advertised again
// on every reconnect and wedge the client in a loop.
func (c *dictionaryCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == c.id {
		c.id, c.dict = "", nil
	}
}
