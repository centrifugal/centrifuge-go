package centrifuge

import "sync"

// dictionaryCache remembers the structure dictionary across a client's
// connections, so a reconnect costs an id rather than a transfer.
//
// Only the structure dictionary is kept, and only the most recent one. Its id
// changes only when a server is upgraded or an operator edits it, so a single
// entry matches on essentially every reconnect. The exception is the window of a
// rolling deploy, where old and new nodes disagree: a client hopping between
// them re-downloads once per hop. That costs about 1.6 KB, is bounded by the
// length of the deploy, and needs no eviction policy to get right.
//
// Channel dictionaries are deliberately not cached. They are built per node from
// a channel's own traffic, so a cached copy would rarely match, and they contain
// verbatim fragments of other users' messages - worth holding for the life of a
// connection, not beyond it.
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

// ids returns what this client holds, to advertise at connect. The protocol
// allows several so a client may keep more; this one keeps the latest.
func (c *dictionaryCache) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == "" {
		return nil
	}
	return []string{c.id}
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
