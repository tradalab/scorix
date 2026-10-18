// Package nativecall is the plugin transport the platform driver installs; only package native calls it.
package nativecall

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

var ErrNoHost = errors.New("no native host")

type request struct {
	ID     uint64          `json:"id"`
	Plugin string          `json:"plugin"`
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type Reply struct {
	ID    uint64          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Code  string          `json:"code,omitempty"`
	Error string          `json:"error,omitempty"`
}

var (
	mu      sync.Mutex
	send    func(call []byte)
	cancel  func(id []byte)
	seq     uint64
	pending = map[uint64]chan Reply{}
)

func Install(sendCall, sendCancel func([]byte)) {
	mu.Lock()
	send, cancel = sendCall, sendCancel
	mu.Unlock()
}

func Call(ctx context.Context, plugin, method string, args json.RawMessage) (Reply, error) {
	mu.Lock()
	if send == nil {
		mu.Unlock()
		return Reply{}, ErrNoHost
	}
	seq++
	id := seq
	ch := make(chan Reply, 1)
	pending[id] = ch
	post, stop := send, cancel
	mu.Unlock()

	msg, err := json.Marshal(request{ID: id, Plugin: plugin, Method: method, Args: args})
	if err != nil {
		forget(id)
		return Reply{}, err
	}
	post(msg)
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		// The host is told so a prompt the user no longer needs goes away;
		// a reply that still arrives finds no one waiting and is dropped.
		if forget(id) {
			b, _ := json.Marshal(struct {
				ID uint64 `json:"id"`
			}{id})
			stop(b)
		}
		return Reply{}, ctx.Err()
	}
}

func forget(id uint64) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := pending[id]
	delete(pending, id)
	return ok
}

func Deliver(raw []byte) {
	var r Reply
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	mu.Lock()
	ch, ok := pending[r.ID]
	delete(pending, r.ID)
	mu.Unlock()
	if ok {
		ch <- r
	}
}
