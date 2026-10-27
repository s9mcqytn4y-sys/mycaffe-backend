package realtime

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Configurable per FRONTEND_URL in production
	},
}

type EventMessage struct {
	Type    string      `json:"type"`     // e.g. "order.created", "order.status_changed", "payment.paid"
	StoreID string      `json:"store_id"` // Multi-tenant scoping
	Payload interface{} `json:"payload"`
}

type Client struct {
	hub     *Hub
	conn    *websocket.Conn
	send    chan []byte
	storeID string
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *EventMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan *EventMessage, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			data, err := json.Marshal(message)
			if err != nil {
				log.Printf("[REALTIME] Error marshaling event: %v", err)
				continue
			}

			h.mu.RLock()
			for client := range h.clients {
				// Only deliver events to matching store (tenant isolation)
				if client.storeID == message.StoreID || message.StoreID == "" {
					select {
					case client.send <- data:
					default:
						close(client.send)
						delete(h.clients, client)
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) BroadcastEvent(eventType, storeID string, payload interface{}) {
	h.broadcast <- &EventMessage{
		Type:    eventType,
		StoreID: storeID,
		Payload: payload,
	}
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, storeID string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[REALTIME] WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		hub:     h,
		conn:    conn,
		send:    make(chan []byte, 64),
		storeID: storeID,
	}
	client.hub.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()

	for message := range c.send {
		w, err := c.conn.NextWriter(websocket.TextMessage)
		if err != nil {
			return
		}
		w.Write(message)

		// Flush queued messages to optimize frames
		n := len(c.send)
		for i := 0; i < n; i++ {
			w.Write([]byte{'\n'})
			w.Write(<-c.send)
		}

		if err := w.Close(); err != nil {
			return
		}
	}
}
