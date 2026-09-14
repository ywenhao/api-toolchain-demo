package tasks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	modelv1 "example.com/api-toolchain-demo/gen/demo/model/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const historyLimit = 128

type event struct {
	id   uint64
	kind string
	data []byte
}

// Broker 保留最近 128 条事件，并隔离慢客户端，防止阻塞 CRUD。
type Broker struct {
	mu      sync.Mutex
	nextID  uint64
	history []event
	clients map[chan event]struct{}
	closed  bool
}

func NewBroker() *Broker { return &Broker{clients: make(map[chan event]struct{})} }

func (b *Broker) Stats() (uint32, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return uint32(len(b.clients)), strconv.FormatUint(b.nextID, 10)
}

func (b *Broker) Publish(kind string, task *modelv1.Task) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.nextID++
	data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(&modelv1.TaskEvent{
		EventId: strconv.FormatUint(b.nextID, 10), Type: kind,
		Task: task, OccurredAt: timestamppb.Now(),
	})
	if err != nil {
		panic(fmt.Sprintf("marshal internally created event: %v", err))
	}
	e := event{id: b.nextID, kind: kind, data: data}
	b.history = append(b.history, e)
	if len(b.history) > historyLimit {
		b.history = append([]event(nil), b.history[len(b.history)-historyLimit:]...)
	}
	for client := range b.clients {
		select {
		case client <- e:
		default:
			close(client)
			delete(b.clients, client)
		}
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for client := range b.clients {
		close(client)
		delete(b.clients, client)
	}
}

func (b *Broker) subscribe(lastID *uint64) (chan event, []event, uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	client := make(chan event, 32)
	if b.closed {
		close(client)
		return client, nil, b.nextID, false
	}
	b.clients[client] = struct{}{}
	var replay []event
	reset := false
	if lastID != nil {
		reset = *lastID > b.nextID || (len(b.history) > 0 && *lastID < b.history[0].id-1)
		if !reset {
			for _, e := range b.history {
				if e.id > *lastID {
					replay = append(replay, e)
				}
			}
		}
	}
	return client, replay, b.nextID, reset
}

func (b *Broker) unsubscribe(client chan event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.clients[client]; ok {
		delete(b.clients, client)
		close(client)
	}
}

// ServeHTTP 提供真正的 SSE；gRPC-Gateway 的 JSON 流不等同于 SSE。
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rawID := r.Header.Get("Last-Event-ID")
	if rawID == "" {
		rawID = r.URL.Query().Get("last_event_id")
	}
	var lastID *uint64
	if rawID != "" {
		id, err := strconv.ParseUint(rawID, 10, 64)
		if err != nil {
			http.Error(w, "Last-Event-ID 必须是非负整数", http.StatusBadRequest)
			return
		}
		lastID = &id
	}
	client, replay, latest, reset := b.subscribe(lastID)
	defer b.unsubscribe(client)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(frame string) error {
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return err
		}
		return controller.Flush()
	}
	ready, _ := json.Marshal(map[string]any{"latestEventId": strconv.FormatUint(latest, 10), "replayed": len(replay)})
	if err := write("retry: 1000\nevent: ready\ndata: " + string(ready) + "\n\n"); err != nil {
		return
	}
	if reset {
		if err := write(fmt.Sprintf("id: %d\nevent: reset\ndata: {\"reason\":\"history-expired\",\"action\":\"reload-tasks\"}\n\n", latest)); err != nil {
			return
		}
	}
	send := func(e event) error {
		return write(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.id, e.kind, e.data))
	}
	for _, e := range replay {
		if err := send(e); err != nil {
			return
		}
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-client:
			if !ok || send(e) != nil {
				return
			}
		case <-ticker.C:
			if write(": heartbeat\n\n") != nil {
				return
			}
		}
	}
}
