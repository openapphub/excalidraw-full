package mcpcanvas

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

const (
	wsSendBuffer = 64
	wsWriteWait  = 10 * time.Second
)

type wsClient struct {
	conn     *websocket.Conn
	send     chan []byte
	done     chan struct{}
	once     sync.Once
	userID   string
	canvasID string
}

func (c *wsClient) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// Hub fans out mutation messages to every connected WebSocket client,
// mirroring the mcp-excalidraw reference server's broadcast mechanism so the
// frontend can live-update the scene without polling.
type Hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: map[*wsClient]struct{}{}}
}

func (h *Hub) Remove(client *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		client.close()
	}
	h.mu.Unlock()
}

// AddWithInitial 原子注册连接并发送初始场景，保证增量事件不会抢在初始场景之前。
func (h *Hub) AddWithInitial(conn *websocket.Conn, userID, canvasID string, msg map[string]interface{}) (*wsClient, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	client := &wsClient{
		conn:     conn,
		send:     make(chan []byte, wsSendBuffer),
		done:     make(chan struct{}),
		userID:   userID,
		canvasID: canvasID,
	}
	h.mu.Lock()
	h.clients[client] = struct{}{}
	// 注册和入队在同一把锁下完成，后续 Broadcast 只能排在初始快照之后。
	client.send <- data
	h.mu.Unlock()
	go h.writePump(client)
	return client, nil
}

func (h *Hub) writePump(client *wsClient) {
	defer client.close()
	for {
		select {
		case <-client.done:
			return
		case data := <-client.send:
			if err := client.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
				h.Remove(client)
				return
			}
			if err := client.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				logrus.WithError(err).Debug("mcpcanvas: ws client dropped")
				h.Remove(client)
				return
			}
		}
	}
}

// BroadcastToCanvas 只向同一 Scene 且当前仍通过 ACL 的连接推送。授权回调在
// Hub 锁外执行，避免数据库查询阻塞其他 Scene 的广播。
func (h *Hub) BroadcastToCanvas(canvasID string, msg map[string]interface{}, authorize func(userID string) bool) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	clients := make([]*wsClient, 0)
	for client := range h.clients {
		if client.canvasID == canvasID {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()

	for _, client := range clients {
		if authorize != nil && !authorize(client.userID) {
			h.Remove(client)
			continue
		}
		h.mu.Lock()
		if _, exists := h.clients[client]; !exists {
			h.mu.Unlock()
			continue
		}
		select {
		case client.send <- data:
		default:
			// 慢客户端不能阻塞其他画布的实时同步。
			delete(h.clients, client)
			client.close()
			logrus.Debug("mcpcanvas: ws client dropped due to full send queue")
		}
		h.mu.Unlock()
	}
}

// ServeWS upgrades an HTTP request to a WebSocket connection and registers
// it with the hub. On connect the client receives the full current scene so
// it can render immediately.
func (s *Store) ServeWS(w http.ResponseWriter, r *http.Request) {
	canvasID := r.URL.Query().Get("canvasId")
	if canvasID == "" {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required")
		return
	}
	if err := s.authorizeCanvas(r, canvasID, false); err != nil {
		writeAccessErr(w, err)
		return
	}
	userID, _ := requestActor(r)
	upgrader := websocket.Upgrader{
		CheckOrigin:  func(r *http.Request) bool { return true },
		Subprotocols: []string{"excalidraw-auth"},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("mcpcanvas: ws upgrade failed")
		return
	}
	logrus.Info("mcpcanvas: ws client connected")

	var (
		client  *wsClient
		initErr error
	)
	func() {
		// 初始快照生成、连接注册和发送期间保持读锁，避免漏掉并发增量事件。
		s.mu.RLock()
		defer s.mu.RUnlock()
		st, canvasOK := s.canvases[canvasID]
		if !canvasOK {
			initErr = fmt.Errorf("canvas %s not found", canvasID)
			return
		}
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
		initMsg := map[string]interface{}{"type": "initial_elements", "canvasId": canvasID, "elements": native}
		client, initErr = s.hub.AddWithInitial(conn, userID, canvasID, initMsg)
	}()
	if initErr != nil {
		logrus.WithError(initErr).Debug("mcpcanvas: failed to send initial scene")
		_ = conn.Close()
		return
	}

	// Read loop: keep the connection alive, drop on error/close.
	go func() {
		defer func() {
			s.hub.Remove(client)
			logrus.Info("mcpcanvas: ws client disconnected")
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
