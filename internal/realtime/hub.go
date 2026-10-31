package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

type EventMessage struct {
	Type    string      `json:"type"`     // e.g. "order.created", "order.status_changed", "payment.paid"
	StoreID string      `json:"store_id"` // Multi-tenant scoping: must NEVER be empty when broadcasting tenant events
	Payload interface{} `json:"payload"`
}

type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	storeID  string
	isClosed int32 // atomic flag to avoid multiple channel close
}

type Hub struct {
	clients        map[*Client]bool
	broadcast      chan *EventMessage
	register       chan *Client
	unregister     chan *Client
	stopCh         chan struct{}
	stopOnce       sync.Once
	mu             sync.RWMutex
	allowedOrigins []string
}

func NewHub(allowedOrigins []string) *Hub {
	return &Hub{
		broadcast:      make(chan *EventMessage, 256),
		register:       make(chan *Client),
		unregister:     make(chan *Client),
		stopCh:         make(chan struct{}),
		clients:        make(map[*Client]bool),
		allowedOrigins: allowedOrigins,
	}
}

func (h *Hub) Run() {
	h.RunWithContext(context.Background())
}

func (h *Hub) RunWithContext(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.closeAllClients()
			return
		case <-h.stopCh:
			h.closeAllClients()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.removeClient(client)
		case message := <-h.broadcast:
			// Multi-tenant isolation: Never broadcast empty store_id to all tenants!
			if message.StoreID == "" {
				log.Println("[SECURITY WARNING] Dropped broadcast with empty StoreID to prevent cross-tenant leak")
				continue
			}

			data, err := json.Marshal(message)
			if err != nil {
				log.Printf("[REALTIME] Error marshaling event: %v", err)
				continue
			}

			h.mu.RLock()
			for client := range h.clients {
				// Strict tenant isolation: client must strictly match message.StoreID
				if client.storeID == message.StoreID {
					select {
					case client.send <- data:
					default:
						// Slow client or blocked channel: safely unregister
						go h.removeClient(client)
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Shutdown() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})
}

func (h *Hub) closeAllClients() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		delete(h.clients, client)
		if atomic.CompareAndSwapInt32(&client.isClosed, 0, 1) {
			close(client.send)
		}
		if client.conn != nil {
			_ = client.conn.Close()
		}
	}
}

func (h *Hub) removeClient(client *Client) {
	h.mu.Lock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		if atomic.CompareAndSwapInt32(&client.isClosed, 0, 1) {
			close(client.send)
		}
	}
	h.mu.Unlock()
}

// BroadcastEvent broadcasts an event strictly to the designated storeID
func (h *Hub) BroadcastEvent(eventType, storeID string, payload interface{}) error {
	if storeID == "" {
		return errors.New("cannot broadcast event without explicit storeID (tenant isolation violation)")
	}

	h.broadcast <- &EventMessage{
		Type:    eventType,
		StoreID: storeID,
		Payload: payload,
	}
	return nil
}

// ServeWS upgrades the HTTP connection to WebSocket with strict origin check and tenant verification
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, authenticatedStoreID string) {
	if authenticatedStoreID == "" {
		http.Error(w, "Unauthorized: missing or invalid tenant authentication", http.StatusUnauthorized)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(req *http.Request) bool {
			origin := req.Header.Get("Origin")
			if origin == "" {
				// Reject originless browser requests if Origin header is expected
				return false
			}
			for _, allowed := range h.allowedOrigins {
				if strings.EqualFold(strings.TrimRight(allowed, "/"), strings.TrimRight(origin, "/")) {
					return true
				}
			}
			return false
		},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[REALTIME] WebSocket upgrade rejected or error: %v", err)
		return
	}

	client := &Client{
		hub:      h,
		conn:     conn,
		send:     make(chan []byte, 64),
		storeID:  authenticatedStoreID,
		isClosed: 0,
	}
	client.hub.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, _ = w.Write(message)

			// Flush queued messages to optimize frames
			n := len(c.send)
			for i := 0; i < n; i++ {
				_, _ = w.Write([]byte{'\n'})
				_, _ = w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
