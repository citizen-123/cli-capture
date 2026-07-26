package capture

import "sync"

// Event describes a change to the Store. The TUI translates these into
// tea.Msg values so the Elm-style update loop stays the single UI writer.
type Event struct {
	Flow *Flow
	Kind EventKind
}

type EventKind int

const (
	FlowAdded EventKind = iota
	FlowUpdated
)

// Store is the thread-safe home for every captured Flow. The proxy writes;
// the UI reads and subscribes. It intentionally keeps flows in insertion order
// so the list pane is stable.
type Store struct {
	mu    sync.RWMutex
	order []string
	byID  map[string]*Flow
	subs  []chan Event
}

func NewStore() *Store {
	return &Store{byID: make(map[string]*Flow)}
}

// Add records a new flow and notifies subscribers.
func (s *Store) Add(f *Flow) {
	s.mu.Lock()
	s.byID[f.ID] = f
	s.order = append(s.order, f.ID)
	s.mu.Unlock()
	s.publish(Event{Flow: f, Kind: FlowAdded})
}

// Touch notifies subscribers that an already-stored flow changed in place.
// The proxy mutates the *Flow directly (it holds the pointer) and then calls
// Touch so the UI knows to re-render.
func (s *Store) Touch(f *Flow) {
	s.publish(Event{Flow: f, Kind: FlowUpdated})
}

// List returns a snapshot of flows in insertion order. Callers get copies of
// the slice, not the underlying flows, so iteration is race-free even while
// the proxy keeps mutating flow contents.
func (s *Store) List() []*Flow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Flow, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byID[id])
	}
	return out
}

// Get returns a flow by id.
func (s *Store) Get(id string) (*Flow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.byID[id]
	return f, ok
}

// Subscribe returns a channel that receives every subsequent Event. The buffer
// keeps a burst of captures from blocking the proxy on a slow UI.
func (s *Store) Subscribe() <-chan Event {
	ch := make(chan Event, 256)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	return ch
}

func (s *Store) publish(ev Event) {
	s.mu.RLock()
	subs := s.subs
	s.mu.RUnlock()
	for _, ch := range subs {
		// Non-blocking: if a subscriber is wedged we drop the event rather
		// than stall traffic. The UI does a full List() on resize/refresh so
		// a dropped incremental update is self-healing.
		select {
		case ch <- ev:
		default:
		}
	}
}
