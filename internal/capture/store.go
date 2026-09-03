package capture

import "sync"

// Event is a compact Store invalidation. Consumers refresh the canonical
// snapshot with List or Get; it deliberately does not carry a captured flow.
// Flow is retained only as a compatibility summary (ID, status, and flag) for
// older consumers and contains no request, response, message, or endpoint data.
type Event struct {
	FlowID  string
	Version uint64
	Kind    EventKind
	Flow    *Flow
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
	mu      sync.RWMutex
	order   []string
	byID    map[string]*Flow
	bytes   map[string]int
	total   int
	version uint64
	subs    []*subscriber
}

// subscriber holds one coalesced invalidation. A slow UI needs only the newest
// version: after any event it reloads Store state, so retaining every
// intermediate capture snapshot is both redundant and unbounded by bytes.
type subscriber struct {
	mu sync.Mutex
	ch chan Event
}

func (sub *subscriber) publish(event Event) {
	sub.mu.Lock()
	defer sub.mu.Unlock()

	select {
	case queued := <-sub.ch:
		if queued.Version > event.Version {
			event = queued
		}
	default:
	}
	sub.ch <- event
}

func NewStore() *Store {
	return &Store{
		byID:  make(map[string]*Flow),
		bytes: make(map[string]int),
	}
}

// Add records an owned snapshot and notifies subscribers. The caller may keep
// mutating f for forwarding; its maps and slices never become Store state.
func (s *Store) Add(f *Flow) {
	if f == nil {
		return
	}
	limitFlow(f)
	owned := f.Snapshot()
	s.mu.Lock()
	_, exists := s.byID[owned.ID]
	if !exists {
		s.order = append(s.order, owned.ID)
	} else {
		s.total -= s.bytes[owned.ID]
	}
	s.byID[owned.ID] = owned
	s.bytes[owned.ID] = flowBytes(owned)
	s.total += s.bytes[owned.ID]
	s.evictLocked()
	kind := FlowAdded
	if exists {
		kind = FlowUpdated
	}
	event := s.eventLocked(owned, kind)
	s.mu.Unlock()

	s.publish(event)
}

// Touch replaces the stored snapshot with the flow's current owned state.
// Producers must use Flow.Mutate for writes that can race another producer.
func (s *Store) Touch(f *Flow) {
	if f == nil {
		return
	}
	limitFlow(f)
	owned := f.Snapshot()
	s.mu.Lock()
	if _, exists := s.byID[owned.ID]; !exists {
		s.order = append(s.order, owned.ID)
	} else {
		s.total -= s.bytes[owned.ID]
	}
	s.byID[owned.ID] = owned
	s.bytes[owned.ID] = flowBytes(owned)
	s.total += s.bytes[owned.ID]
	s.evictLocked()
	event := s.eventLocked(owned, FlowUpdated)
	s.mu.Unlock()

	s.publish(event)
}

func (s *Store) evictLocked() {
	for len(s.order) > MaxFlowsInStore || (s.total > MaxRetainedStoreBytes && len(s.order) > 1) {
		id := s.order[0]
		delete(s.byID, id)
		s.total -= s.bytes[id]
		delete(s.bytes, id)
		copy(s.order, s.order[1:])
		s.order[len(s.order)-1] = ""
		s.order = s.order[:len(s.order)-1]
	}
}

// SetFlagged changes the canonical stored snapshot rather than a caller-owned
// copy, then publishes a compact invalidation.
func (s *Store) SetFlagged(id string, flagged bool) bool {
	s.mu.Lock()
	f, ok := s.byID[id]
	var event Event
	if ok {
		f.Flagged = flagged
		event = s.eventLocked(f, FlowUpdated)
	}
	s.mu.Unlock()
	if ok {
		s.publish(event)
	}
	return ok
}

// List returns deep snapshots in insertion order. Callers cannot mutate Store
// state or race a producer by retaining a returned Flow.
func (s *Store) List() []*Flow {
	s.mu.RLock()
	out := make([]*Flow, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, cloneFlow(s.byID[id]))
	}
	s.mu.RUnlock()
	return out
}

// Get returns an independent deep snapshot of the flow identified by id.
func (s *Store) Get(id string) (*Flow, bool) {
	s.mu.RLock()
	f, ok := s.byID[id]
	out := cloneFlow(f)
	s.mu.RUnlock()
	return out, ok
}

// Subscribe returns a channel that receives a coalesced latest-version
// invalidation. Its one-event capacity is independent of capture size, so a
// stalled consumer cannot duplicate Store's retained bodies.
func (s *Store) Subscribe() <-chan Event {
	sub := &subscriber{ch: make(chan Event, 1)}
	s.mu.Lock()
	s.subs = append(s.subs, sub)
	s.mu.Unlock()
	return sub.ch
}

func (s *Store) eventLocked(flow *Flow, kind EventKind) Event {
	s.version++
	return Event{
		FlowID:  flow.ID,
		Version: s.version,
		Kind:    kind,
		Flow: &Flow{
			ID:      flow.ID,
			Status:  flow.Status,
			Flagged: flow.Flagged,
		},
	}
}

func (s *Store) publish(event Event) {
	s.mu.RLock()
	subs := append([]*subscriber(nil), s.subs...)
	s.mu.RUnlock()
	for _, sub := range subs {
		// Give each consumer an independent metadata summary. No full flow is
		// ever enqueued, and subscriber.publish replaces stale invalidations.
		copy := event
		if event.Flow != nil {
			flow := *event.Flow
			copy.Flow = &flow
		}
		sub.publish(copy)
	}
}
