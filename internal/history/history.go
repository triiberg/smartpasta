package history

import (
	"errors"
	"sync"
	"time"
)

const (
	DefaultMaxEntries = 20
	DefaultMaxBytes   = 1 << 20
)

var ErrNotFound = errors.New("entry not found")

type Entry struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type History struct {
	mu       sync.Mutex
	entries  []Entry
	max      int
	maxBytes int
	nextID   int64
}

func New(maxEntries int, maxBytes int) *History {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &History{
		max:      maxEntries,
		maxBytes: maxBytes,
		nextID:   1,
	}
}

func (h *History) Add(content string) (Entry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if content == "" {
		return Entry{}, false
	}
	if len(content) > h.maxBytes {
		return Entry{}, false
	}
	if len(h.entries) > 0 && h.entries[0].Content == content {
		return Entry{}, false
	}

	entry := Entry{
		ID:        h.nextID,
		Content:   content,
		CreatedAt: time.Now(),
	}
	h.nextID++

	h.entries = append([]Entry{entry}, h.entries...)
	if len(h.entries) > h.max {
		h.entries = h.entries[:h.max]
	}
	return entry, true
}

func (h *History) ListMRU() []Entry {
	h.mu.Lock()
	defer h.mu.Unlock()

	entries := make([]Entry, len(h.entries))
	copy(entries, h.entries)
	return entries
}

func (h *History) ListChronological() []Entry {
	h.mu.Lock()
	defer h.mu.Unlock()

	entries := make([]Entry, len(h.entries))
	for i := range h.entries {
		entries[len(h.entries)-1-i] = h.entries[i]
	}
	return entries
}

func (h *History) Select(id int64) (Entry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i, entry := range h.entries {
		if entry.ID == id {
			if i == 0 {
				return entry, nil
			}
			h.entries = append([]Entry{entry}, append(h.entries[:i], h.entries[i+1:]...)...)
			return entry, nil
		}
	}
	return Entry{}, ErrNotFound
}

func (h *History) Delete(id int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i, entry := range h.entries {
		if entry.ID == id {
			h.entries = append(h.entries[:i], h.entries[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries = nil
}
