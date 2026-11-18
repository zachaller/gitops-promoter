package webserver

import (
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
)

// Names of the server-sent events on the /watch stream, besides the per-kind update event
// (named after the resource Kind, e.g. "PromotionStrategyDetails").
const (
	// eventSnapshot carries the complete current state for the subscription as a JSON array.
	// It is the first event on every connection, so a client that (re)connects replaces its
	// state with the snapshot and cannot have missed anything while it was away.
	eventSnapshot = "snapshot"
	// eventDeleted reports that an object no longer exists.
	eventDeleted = "deleted"
	// eventPing is the periodic heartbeat that keeps idle connections open through proxies and
	// lets the browser notice a connection that died without being closed.
	eventPing = "ping"
)

// Message is one change to broadcast to subscribed clients.
type Message struct {
	// Name, Namespace and Kind identify the changed object.
	Name      string
	Namespace string
	Kind      string
	// Data is the JSON payload: the full object for an update, an objectRef for a deletion.
	Data string
	// Deleted is true when the object was deleted.
	Deleted bool
}

// objectRef identifies a deleted object in the payload of an eventDeleted event.
type objectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// key identifies the object a message is about. Kubernetes kinds, namespaces and names
// cannot contain a slash, so the joined form is unambiguous.
func (m Message) key() string {
	return strings.ToLower(m.Kind + "/" + m.Namespace + "/" + m.Name)
}

// event returns the SSE event name the message is sent under.
func (m Message) event() string {
	if m.Deleted {
		return eventDeleted
	}
	return m.Kind
}

// watchFilter selects the objects a subscriber is interested in. Each part is a glob
// (path.Match syntax) and an empty part matches everything.
type watchFilter struct {
	pattern string
}

// newWatchFilter builds a filter from the kind, namespace and name query parameters. It
// returns an error for a malformed glob so the request can be rejected up front.
func newWatchFilter(kind, namespace, name string) (watchFilter, error) {
	orAny := func(s string) string {
		if s == "" {
			return "*"
		}
		return strings.ToLower(s)
	}
	f := watchFilter{pattern: orAny(kind) + "/" + orAny(namespace) + "/" + orAny(name)}
	if _, err := path.Match(f.pattern, ""); err != nil {
		return watchFilter{}, fmt.Errorf("invalid watch filter %q: %w", f.pattern, err)
	}
	return f, nil
}

// matches reports whether the object identified by kind, namespace and name is selected.
func (f watchFilter) matches(kind, namespace, name string) bool {
	// The pattern was validated by newWatchFilter, so Match cannot fail here.
	ok, _ := path.Match(f.pattern, strings.ToLower(kind+"/"+namespace+"/"+name))
	return ok
}

// subscriber is one connected /watch client.
//
// Changes are queued per object and a newer change to an object replaces an undelivered
// older one. Every message carries the object's complete state, so this never loses
// information the client needs: however far behind the client is, it ends on the latest
// state of every object. It also bounds the queue to one entry per object, so a slow
// client cannot grow it without limit and the broadcaster never has to block or drop.
type subscriber struct {
	// pending holds the latest undelivered message for each object.
	pending map[string]Message
	// notify wakes the stream handler when pending becomes non-empty.
	notify chan struct{}
	filter watchFilter
	// order lists the keys of pending in the order they first became pending.
	order []string
	mu    sync.Mutex
}

func newSubscriber(filter watchFilter) *subscriber {
	return &subscriber{
		filter:  filter,
		pending: map[string]Message{},
		notify:  make(chan struct{}, 1),
	}
}

// enqueue queues a message for delivery, replacing any undelivered message for the same
// object. It never blocks.
func (s *subscriber) enqueue(m Message) {
	key := m.key()
	s.mu.Lock()
	if _, queued := s.pending[key]; !queued {
		s.order = append(s.order, key)
	}
	s.pending[key] = m
	s.mu.Unlock()

	select {
	case s.notify <- struct{}{}:
	default:
		// A wake-up is already pending; the handler drains everything when it runs.
	}
}

// drain returns the queued messages in the order their objects first became pending and
// empties the queue.
func (s *subscriber) drain() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return nil
	}
	out := make([]Message, 0, len(s.order))
	for _, key := range s.order {
		out = append(out, s.pending[key])
	}
	s.order = nil
	s.pending = map[string]Message{}
	return out
}

// broadcaster fans changes out to the connected /watch clients.
type broadcaster struct {
	subscribers map[*subscriber]struct{}
	mu          sync.RWMutex
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subscribers: map[*subscriber]struct{}{}}
}

// subscribe registers a new client. Changes published from this point on are queued for it.
func (b *broadcaster) subscribe(filter watchFilter) *subscriber {
	s := newSubscriber(filter)
	b.mu.Lock()
	b.subscribers[s] = struct{}{}
	count := len(b.subscribers)
	b.mu.Unlock()
	logger.Info("watch client connected", "clientCount", count)
	return s
}

// unsubscribe removes a client.
func (b *broadcaster) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subscribers, s)
	count := len(b.subscribers)
	b.mu.Unlock()
	logger.Info("watch client disconnected", "clientCount", count)
}

// publish queues a change for every subscriber whose filter selects it.
func (b *broadcaster) publish(m Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subscribers {
		if s.filter.matches(m.Kind, m.Namespace, m.Name) {
			s.enqueue(m)
		}
	}
}

// subscriberCount returns the number of connected clients.
func (b *broadcaster) subscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// writeSSE writes one server-sent event.
func writeSSE(w io.Writer, event, data string) error {
	// A line break inside the payload would end the data field early, so each line gets its
	// own field. Marshaled JSON never contains a raw line break; this is a safety net.
	data = strings.ReplaceAll(data, "\n", "\ndata:")
	if _, err := fmt.Fprintf(w, "event:%s\ndata:%s\n\n", event, data); err != nil {
		return fmt.Errorf("failed to write %s event: %w", event, err)
	}
	return nil
}
