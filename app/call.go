package app

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/tradalab/scorix/fault"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/webview"
)

type pendingCall struct {
	client ClientID
	ch     chan webview.Message
}

func (a *App) Call(ctx context.Context, client ClientID, name string, payload any) (json.RawMessage, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	id := "call-" + strconv.FormatUint(a.seq.Add(1), 10)

	ch := make(chan webview.Message, 1)
	a.mu.Lock()
	s, connected := a.senders[int(client)]
	if connected {
		if a.calls == nil {
			a.calls = map[string]pendingCall{}
		}
		a.calls[id] = pendingCall{client: client, ch: ch}
	}
	a.mu.Unlock()
	if !connected {
		return nil, fault.Errorf(fault.CodeUnavailable, "client %d is not connected", client)
	}
	defer func() {
		a.mu.Lock()
		delete(a.calls, id)
		a.mu.Unlock()
	}()

	msg, err := json.Marshal(webview.Message{ID: id, Kind: "call", Name: name, State: "start", Data: data})
	if err != nil {
		return nil, err
	}
	s.enqueue(msg)

	select {
	case <-ctx.Done():
		return nil, fault.Wrap(fault.CodeCanceled, ctx.Err())
	case reply := <-ch:
		if reply.State == "error" {
			code := reply.ErrorCode
			if code == "" {
				code = "call_failed"
			}
			return nil, fault.New(code, reply.Error)
		}
		return reply.Data, nil
	}
}

func (a *App) handleCallReply(from ipc.ClientID, msg webview.Message) {
	a.mu.Lock()
	pc, ok := a.calls[msg.ID]
	a.mu.Unlock()
	if !ok || pc.client != from { // ids are guessable; only the addressed client may answer
		return
	}
	select {
	case pc.ch <- msg:
	default:
	}
}

func (a *App) failCallsFor(client ClientID) {
	a.mu.Lock()
	var chans []chan webview.Message
	for id, pc := range a.calls {
		if pc.client == client {
			chans = append(chans, pc.ch)
			delete(a.calls, id)
		}
	}
	a.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- webview.Message{State: "error", Error: "client disconnected", ErrorCode: fault.CodeUnavailable}:
		default:
		}
	}
}
