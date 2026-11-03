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

// Channels a client can subscribe to (iteration 5c). Guests get "guest.<guest_session_id>".
const (
	ChannelPOS         = "pos"
	ChannelKDSBar      = "kds.bar"
	ChannelKDSKitchen  = "kds.kitchen"
	ChannelGuestPrefix = "guest."
)

// EventMessage is one outbox event as sent to WebSocket clients. Seq is the per-store outbox
// sequence; clients keep the highest seq seen and send it back as last_seq when reconnecting.
type EventMessage struct {
	Seq     int64       `json:"seq,omitempty"`
	Type    string      `json:"type"`
	StoreID string      `json:"store_id"` // Multi-tenant scoping: must NEVER be empty when broadcasting tenant events
	Payload interface{} `json:"payload"`
	// Channels routes the message; a client receives it when subscribed to any of them.
	Channels []string `json:"-"`
}

// Subscription is what a redeemed WebSocket ticket grants.
type Subscription struct {
	StoreID  string
	Channels []string
	// Replay returns missed events. It runs after registration so nothing published meanwhile is
	// lost; a duplicate is possible and harmless because clients deduplicate by seq.
	Replay func() ([]*EventMessage, error)
	// Alive re-checks the session behind the ticket; the connection closes once it reports false.
	Alive func(ctx context.Context) (bool, error)
	// IP and GuestKey (guest session id, empty for staff) count against the connection caps.
	IP       string
	GuestKey string
}

type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	storeID  string
	channels map[string]bool
	ip       string
	guestKey string
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
	limits         Limits
	revalidate     time.Duration
}

// Limits caps concurrent connections per guest session, client IP and store; zero disables a cap.
type Limits struct {
	PerGuest, PerIP, PerStore int
}

// ErrTooManyConnections is returned by Admit when a connection cap is reached.
var ErrTooManyConnections = errors.New("too many realtime connections")

// Defaults for one cafe: a few tabs per guest, one counter's devices behind one IP, all screens of a store.
var DefaultLimits = Limits{PerGuest: 3, PerIP: 20, PerStore: 150}

// DefaultRevalidateInterval is how often an open connection re-checks its session.
const DefaultRevalidateInterval = 30 * time.Second

func NewHub(allowedOrigins []string) *Hub {
	return &Hub{
		broadcast:      make(chan *EventMessage, 256),
		register:       make(chan *Client),
		unregister:     make(chan *Client),
		stopCh:         make(chan struct{}),
		clients:        make(map[*Client]bool),
		allowedOrigins: allowedOrigins,
		limits:         DefaultLimits,
		revalidate:     DefaultRevalidateInterval,
	}
}

func (h *Hub) Run() {
	h.RunWithContext(context.Background())
}

func (h *Hub) RunWithContext(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.Shutdown() // stopCh is the single signal senders select on once the loop exits
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
				// Strict tenant isolation plus channel routing.
				if client.storeID == message.StoreID && client.subscribed(message.Channels) {
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

func (c *Client) subscribed(channels []string) bool {
	for _, ch := range channels {
		if c.channels[ch] {
			return true
		}
	}
	return false
}

// BroadcastEvent sends an event to the store's POS channel.
func (h *Hub) BroadcastEvent(eventType, storeID string, payload interface{}) error {
	return h.Publish(&EventMessage{Type: eventType, StoreID: storeID, Payload: payload, Channels: []string{ChannelPOS}})
}

// Publish routes a message to the clients of its store that subscribe to one of its channels.
func (h *Hub) Publish(msg *EventMessage) error {
	if msg == nil || msg.StoreID == "" {
		return errors.New("cannot broadcast event without explicit storeID (tenant isolation violation)")
	}
	select {
	case h.broadcast <- msg:
	case <-h.stopCh:
		return errors.New("realtime hub is shut down")
	}
	return nil
}

// SetLimits replaces the connection caps and the session re-check interval (call before serving).
func (h *Hub) SetLimits(limits Limits, revalidate time.Duration) {
	h.limits, h.revalidate = limits, revalidate
}

// Admit reports ErrTooManyConnections when sub would exceed a cap. Checked before the upgrade, so two
// simultaneous connects may overshoot a cap by one.
func (h *Hub) Admit(sub Subscription) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var perGuest, perIP, perStore int
	for c := range h.clients {
		if c.storeID != sub.StoreID {
			continue
		}
		perStore++
		if sub.IP != "" && c.ip == sub.IP {
			perIP++
		}
		if sub.GuestKey != "" && c.guestKey == sub.GuestKey {
			perGuest++
		}
	}
	l := h.limits
	if (l.PerStore > 0 && perStore >= l.PerStore) || (l.PerIP > 0 && perIP >= l.PerIP) || (l.PerGuest > 0 && perGuest >= l.PerGuest) {
		return ErrTooManyConnections
	}
	return nil
}

// ServeWS upgrades an authenticated subscription (store and channels from a redeemed ticket) with a
// strict Origin check, then replays missed events.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, sub Subscription) {
	if sub.StoreID == "" || len(sub.Channels) == 0 {
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
		storeID:  sub.StoreID,
		ip:       sub.IP,
		guestKey: sub.GuestKey,
		channels: map[string]bool{},
		isClosed: 0,
	}
	for _, ch := range sub.Channels {
		client.channels[ch] = true
	}
	select {
	case client.hub.register <- client:
	case <-h.stopCh:
		_ = conn.Close()
		return
	}

	go client.writePump()
	go client.readPump()
	if sub.Replay != nil {
		go client.replay(sub.Replay)
	}
	if sub.Alive != nil && h.revalidate > 0 {
		go client.watch(sub.Alive, h.revalidate)
	}
}

// watch closes the connection once its session is revoked, logged out, deactivated or ended. A failed
// check (database unavailable) keeps the connection; the next tick tries again.
func (c *Client) watch(alive func(ctx context.Context) (bool, error), every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-c.hub.stopCh:
			return
		case <-ticker.C:
		}
		if atomic.LoadInt32(&c.isClosed) == 1 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ok, err := alive(ctx)
		cancel()
		if err != nil {
			log.Printf("[REALTIME] session re-check failed for store %s: %v", c.storeID, err)
			continue
		}
		if !ok {
			c.hub.removeClient(c)
			return
		}
	}
}

// replay queues missed events behind the live stream; a client too slow to take them is dropped.
func (c *Client) replay(load func() ([]*EventMessage, error)) {
	msgs, err := load()
	if err != nil {
		log.Printf("[REALTIME] replay failed for store %s: %v", c.storeID, err)
		return
	}
	for _, m := range msgs {
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		c.hub.mu.RLock()
		_, alive := c.hub.clients[c]
		if alive {
			select {
			case c.send <- data:
			default:
				alive = false
			}
		}
		c.hub.mu.RUnlock()
		if !alive {
			c.hub.removeClient(c)
			return
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		select {
		case c.hub.unregister <- c:
		case <-c.hub.stopCh:
		}
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

			// One event per frame so clients can parse each message as JSON.
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
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
