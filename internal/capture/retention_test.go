package capture

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestStoreBoundsDirectFlowData(t *testing.T) {
	flow := NewFlow("client", "server:443")
	flow.Request = &Message{
		Body: bytes.Repeat([]byte{'b'}, MaxRetainedLogicalBodyBytes+1),
		Raw:  bytes.Repeat([]byte{'r'}, MaxRetainedMessageBytes+1),
	}
	flow.Messages = make([]*Message, MaxMessagesPerFlow+1)
	for i := range flow.Messages {
		flow.Messages[i] = &Message{Body: []byte("message"), Raw: []byte("message")}
	}

	store := NewStore()
	store.Add(flow)

	stored, ok := store.Get(flow.ID)
	if !ok {
		t.Fatal("flow was not stored")
	}
	if !stored.Truncated || !stored.Request.Truncated {
		t.Fatal("over-budget flow was not marked truncated")
	}
	if got := len(stored.Request.Body); got != MaxRetainedLogicalBodyBytes {
		t.Errorf("retained logical bytes = %d, want %d", got, MaxRetainedLogicalBodyBytes)
	}
	if got := len(stored.Request.Raw); got != MaxRetainedMessageBytes {
		t.Errorf("retained wire bytes = %d, want %d", got, MaxRetainedMessageBytes)
	}
	if got := len(stored.Messages); got != MaxMessagesPerFlow {
		t.Errorf("retained messages = %d, want %d", got, MaxMessagesPerFlow)
	}
}

func TestStoreBoundsHeaderAndMetadataRetention(t *testing.T) {
	flow := NewFlow("client", "server:443")
	flow.Request = &Message{
		Headers: map[string][]string{"X-Long": {string(bytes.Repeat([]byte{'h'}, MaxRetainedHeaderBytes))}},
		Meta:    map[string]string{"detail": string(bytes.Repeat([]byte{'m'}, MaxRetainedMetaBytes))},
	}
	store := NewStore()
	store.Add(flow)
	stored, _ := store.Get(flow.ID)
	if !stored.Truncated || !stored.Request.Truncated {
		t.Fatal("over-budget metadata was not marked truncated")
	}
	if got := headerBytes(stored.Request.Headers); got > MaxRetainedHeaderBytes {
		t.Fatalf("stored headers = %d bytes, limit %d", got, MaxRetainedHeaderBytes)
	}
	if got := metaBytes(stored.Request.Meta); got > MaxRetainedMetaBytes {
		t.Fatalf("stored metadata = %d bytes, limit %d", got, MaxRetainedMetaBytes)
	}
}

func TestStoreEvictsOldestFlowsAtRetentionLimit(t *testing.T) {
	store := NewStore()
	var oldestID string
	for i := 0; i <= MaxFlowsInStore; i++ {
		flow := NewFlow("client", fmt.Sprintf("server-%d:443", i))
		if i == 0 {
			oldestID = flow.ID
		}
		store.Add(flow)
	}
	if got := len(store.List()); got != MaxFlowsInStore {
		t.Fatalf("stored flows = %d, want %d", got, MaxFlowsInStore)
	}
	if _, ok := store.Get(oldestID); ok {
		t.Error("oldest flow remained after store retention limit")
	}
}

func TestAddMessageStopsRetainingAfterLimit(t *testing.T) {
	flow := NewFlow("client", "server:443")
	for i := 0; i <= MaxMessagesPerFlow; i++ {
		flow.AddMessage(&Message{Body: []byte("message"), Raw: []byte("message")})
	}
	if got := len(flow.Messages); got != MaxMessagesPerFlow {
		t.Fatalf("retained messages = %d, want %d", got, MaxMessagesPerFlow)
	}
	if !flow.Truncated {
		t.Fatal("flow was not marked truncated after dropping a message")
	}
}

func TestStoreSnapshotsAreDeepAndEventsAreMetadataOnly(t *testing.T) {
	headers := map[string][]string{"X-Test": {"before"}}
	meta := map[string]string{"state": "before"}
	flow := NewFlow("client", "server:443")
	flow.Request = &Message{Headers: headers, Body: []byte("before"), Raw: []byte("before"), Meta: meta}
	store := NewStore()
	events := store.Subscribe()
	store.Add(flow)

	flow.Request.Headers["X-Test"][0] = "after"
	flow.Request.Body[0] = 'A'
	flow.Request.Raw[0] = 'A'
	flow.Request.Meta["state"] = "after"
	stored, ok := store.Get(flow.ID)
	if !ok {
		t.Fatal("stored flow missing")
	}
	if got := stored.Request.Headers["X-Test"][0]; got != "before" {
		t.Fatalf("stored header = %q, want independent snapshot", got)
	}
	if got := string(stored.Request.Body); got != "before" {
		t.Fatalf("stored body = %q, want independent snapshot", got)
	}
	if got := stored.Request.Meta["state"]; got != "before" {
		t.Fatalf("stored meta = %q, want independent snapshot", got)
	}

	listed := store.List()[0]
	listed.Request.Headers["X-Test"][0] = "list mutation"
	event := <-events
	if event.FlowID != flow.ID || event.Version == 0 || event.Flow == nil {
		t.Fatalf("event = %+v, want metadata for %q", event, flow.ID)
	}
	if event.Flow.Request != nil || event.Flow.Response != nil || len(event.Flow.Messages) != 0 {
		t.Fatalf("event retained captured data: %+v", event.Flow)
	}
	event.Flow.Status = StatusError
	again, _ := store.Get(flow.ID)
	if got := again.Request.Headers["X-Test"][0]; got != "before" {
		t.Fatalf("snapshot mutation escaped into Store: %q", got)
	}
}

func TestStoreSubscriberCoalescesLatestMetadataEvent(t *testing.T) {
	store := NewStore()
	events := store.Subscribe()
	if got := cap(events); got != 1 {
		t.Fatalf("subscriber capacity = %d, want one coalesced invalidation", got)
	}

	flow := NewFlow("client", "server:443")
	flow.Request = &Message{Body: bytes.Repeat([]byte{'b'}, MaxRetainedLogicalBodyBytes)}
	store.Add(flow)
	const updates = 32
	for i := 0; i < updates; i++ {
		flow.Mutate(func() { flow.Status = Status(i % 4) })
		store.Touch(flow)
	}

	event := <-events
	if want := uint64(updates + 1); event.Version != want {
		t.Fatalf("coalesced event version = %d, want latest %d", event.Version, want)
	}
	if event.FlowID != flow.ID || event.Flow == nil || event.Flow.Status != flow.Status {
		t.Fatalf("coalesced event = %+v, want latest metadata for %q", event, flow.ID)
	}
	if event.Flow.Request != nil || event.Flow.Response != nil || len(event.Flow.Messages) != 0 {
		t.Fatalf("queued event retained captured data: %+v", event.Flow)
	}
}

func TestFlowAggregateRetentionCapsStreamingBytes(t *testing.T) {
	flow := NewFlow("client", "server:443")
	for range 20 {
		flow.AddMessage(&Message{
			Body: bytes.Repeat([]byte{'b'}, MaxFrameBytes),
			Raw:  bytes.Repeat([]byte{'r'}, MaxFrameBytes),
		})
	}
	if got := flowBytes(flow); got > MaxRetainedFlowBytes {
		t.Fatalf("flow retained %d bytes, budget is %d", got, MaxRetainedFlowBytes)
	}
	if !flow.Truncated {
		t.Fatal("aggregate retention did not mark the flow truncated")
	}
}

func TestStoreAggregateRetentionEvictsOldFlows(t *testing.T) {
	store := NewStore()
	var first string
	for i := range 10 {
		flow := NewFlow("client", fmt.Sprintf("server-%d:443", i))
		flow.Request = &Message{Body: bytes.Repeat([]byte{'b'}, MaxRetainedLogicalBodyBytes), Raw: bytes.Repeat([]byte{'r'}, MaxRetainedMessageBytes)}
		if i == 0 {
			first = flow.ID
		}
		store.Add(flow)
	}
	if _, ok := store.Get(first); ok {
		t.Fatal("store retained oldest flow beyond aggregate budget")
	}
}

func TestStoreSnapshotsRemainIndependentDuringConcurrentTouches(t *testing.T) {
	flow := NewFlow("client", "server:443")
	flow.Request = &Message{Headers: map[string][]string{"X": {"0"}}, Meta: map[string]string{"n": "0"}}
	store := NewStore()
	store.Add(flow)

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := range 100 {
			flow.Mutate(func() {
				flow.Request.Headers["X"][0] = fmt.Sprint(i)
				flow.Request.Meta["n"] = fmt.Sprint(i)
				flow.Request.Body = []byte(fmt.Sprint(i))
			})
			store.Touch(flow)
		}
	}()
	go func() {
		defer workers.Done()
		for range 100 {
			snapshot := store.List()[0]
			snapshot.Request.Headers["X"][0] = "reader"
			snapshot.Request.Meta["n"] = "reader"
		}
	}()
	workers.Wait()
	got, ok := store.Get(flow.ID)
	if !ok || got.Request == nil {
		t.Fatal("stored flow was lost during concurrent updates")
	}
	if got.Request.Meta["n"] == "reader" {
		t.Fatal("reader mutation escaped a Store snapshot")
	}
}
