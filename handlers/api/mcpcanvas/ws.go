package mcpcanvas

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// Hub fans out mutation messages to every connected WebSocket client,
// mirroring the mcp-excalidraw reference server's broadcast mechanism so the
// frontend can live-update the scene without polling.
type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
}

func NewHub() *Hub {
	return &Hub{clients: map[*websocket.Conn]bool{}}
}

func (h *Hub) Add(conn *websocket.Conn) {
	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()
}

func (h *Hub) Remove(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
}

// Broadcast sends a JSON message to all connected clients. Dead connections
// are dropped.
func (h *Hub) Broadcast(msg map[string]interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
			delete(h.clients, conn)
			logrus.WithError(err).Debug("mcpcanvas: ws client dropped")
		}
	}
}

// ServeWS upgrades an HTTP request to a WebSocket connection and registers
// it with the hub. On connect the client receives the full current scene so
// it can render immediately.
func (s *Store) ServeWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("mcpcanvas: ws upgrade failed")
		return
	}
	s.hub.Add(conn)
	logrus.Info("mcpcanvas: ws client connected")

	// Send initial scene in native format (frontend renders native elements).
	// Includes the canvasId so the frontend can sync the right canvas.
	s.mu.RLock()
	cur := s.current
	st := s.canvases[cur]
	var all []map[string]interface{}
	if st != nil {
		all = make([]map[string]interface{}, 0, len(st.order))
		for _, id := range st.order {
			all = append(all, st.elements[id])
		}
	}
	s.mu.RUnlock()
	native := make([]map[string]interface{}, 0, len(all)*2)
	for _, el := range all {
		n, textEl := agentToNative(el)
		native = append(native, n)
		if textEl != nil {
			native = append(native, textEl)
		}
	}
	// Critical: resolve arrow paths BEFORE pushing, exactly like
	// persistCanvasLocked does. Missing this made WS clients receive
	// arrows at (0,0) and overwrite the correct scene on load.
	resolveArrowBindings(native)
	initMsg := map[string]interface{}{"type": "initial_elements", "canvasId": cur, "elements": native}
	if data, err := json.Marshal(initMsg); err == nil {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}

	// Read loop: keep the connection alive, drop on error/close.
	go func() {
		defer func() {
			s.hub.Remove(conn)
			conn.Close()
			logrus.Info("mcpcanvas: ws client disconnected")
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
