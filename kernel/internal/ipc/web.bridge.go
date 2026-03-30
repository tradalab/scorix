package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/tradalab/scorix/logger"
)

type WebBridge struct {
	exec     func(ctx context.Context, msg Message) Message
	mu       sync.Mutex
	conns    map[*websocket.Conn]struct{}
	upgrader websocket.Upgrader
}

func NewWebBridge() *WebBridge {
	return &WebBridge{
		conns: make(map[*websocket.Conn]struct{}),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all for now
			},
		},
	}
}

func (b *WebBridge) Name() string {
	return "web"
}

func (b *WebBridge) OnMessage(exec func(ctx context.Context, msg Message) Message) error {
	b.exec = exec
	return nil
}

func (b *WebBridge) Emit(ctx context.Context, msg Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for conn := range b.conns {
		if err := conn.WriteJSON(msg); err != nil {
			logger.Debug("WebBridge: Emit write failed, client likely disconnected", logger.Err(err))
			// connection will be cleaned up in its own read loop handler
		}
	}
	return nil
}

func (b *WebBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("WebBridge: Upgrade failed", logger.Err(err))
		return
	}

	b.mu.Lock()
	b.conns[conn] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.conns, conn)
		b.mu.Unlock()
		conn.Close()
	}()

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.Debug("WebBridge: Connection closed", logger.Err(err))
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(payload, &msg); err != nil {
			logger.Error("WebBridge: Message unmarshal error", logger.Err(err))
			continue
		}

		if b.exec != nil {
			go func() {
				// Process message and send response back over the SAME socket
				result := b.exec(context.Background(), msg)
				if result.Id != "" {
					b.mu.Lock()
					_ = conn.WriteJSON(result)
					b.mu.Unlock()
				}
			}()
		}
	}
}
