// Package hub fans events out to a user's connected clients.
package hub

import "sync"

// Event tells clients that a message arrived ("message") or changed
// ("update", "delete"). Clients fetch the message itself from the API, so
// no mail content travels through the hub.
type Event struct {
	UserID    int64  `json:"-"`
	Type      string `json:"type"`
	MessageID int64  `json:"message_id"`
}

type Hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan Event]struct{}
}

func New() *Hub { return &Hub{subs: make(map[int64]map[chan Event]struct{})} }

// Subscribe returns a channel of the user's events and a cancel function.
func (h *Hub) Subscribe(userID int64) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[chan Event]struct{})
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[userID], ch)
		if len(h.subs[userID]) == 0 {
			delete(h.subs, userID)
		}
		h.mu.Unlock()
	}
}

// Publish never blocks: a client that falls behind misses the live event
// and catches up from the message list.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[e.UserID] {
		select {
		case ch <- e:
		default:
		}
	}
}
