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

func (h *Hub) Remove(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
}

// AddWithInitial 原子注册连接并发送初始场景，保证增量事件不会抢在初始场景之前。
func (h *Hub) AddWithInitial(conn *websocket.Conn, msg map[string]interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = true
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		conn.Close()
		delete(h.clients, conn)
		return err
	}
	return nil
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
	logrus.Info("mcpcanvas: ws client connected")

	var initErr error
	func() {
		// 初始快照生成、连接注册和发送期间保持读锁，避免漏掉并发增量事件。
		s.mu.RLock()
		defer s.mu.RUnlock()
		cur := s.current
		st := s.canvases[cur]
		var all []map[string]interface{}
		if st != nil {
			all = make([]map[string]interface{}, 0, len(st.order))
			for _, id := range st.order {
				all = append(all, st.elements[id])
			}
		}
		native := make([]map[string]interface{}, 0, len(all)*2)
		for _, el := range all {
			n, textEl := agentToNative(el)
			native = append(native, n)
			if textEl != nil {
				native = append(native, textEl)
			}
		}
		// 推送前必须解析箭头路径，与 persistCanvasLocked 保持一致。
		resolveArrowBindings(native)
		initMsg := map[string]interface{}{"type": "initial_elements", "canvasId": cur, "elements": native}
		initErr = s.hub.AddWithInitial(conn, initMsg)
	}()
	if initErr != nil {
		logrus.WithError(initErr).Debug("mcpcanvas: failed to send initial scene")
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
