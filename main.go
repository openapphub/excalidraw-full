package main

import (
	"embed"
	_ "embed"
	"encoding/json"
	"excalidraw-complete/handlers/api/documents"
	"excalidraw-complete/handlers/api/firebase"
	"excalidraw-complete/handlers/api/kv"
	"excalidraw-complete/handlers/api/mcpcanvas"
	"excalidraw-complete/handlers/api/openai"
	"excalidraw-complete/handlers/auth"
	authMiddleware "excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"github.com/zishang520/engine.io/v2/types"
	"github.com/zishang520/engine.io/v2/utils"
	socketio "github.com/zishang520/socket.io/v2/socket"
)

type (
	UserToFollow struct {
		SocketId string `json:"socketId"`
		Username string `json:"username"`
	}

	OnUserFollowedPayload struct {
		UserToFollow UserToFollow `json:"userToFollow"`
		Action       string       `json:"action"` // "FOLLOW" | "UNFOLLOW"
	}
)

//go:embed all:frontend
var assets embed.FS

func handleUI() http.HandlerFunc {
	sub, err := fs.Sub(assets, "frontend")
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// If the path is empty, it means it's the root, so serve index.html
		if path == "/" || path == "" {
			path = "/index.html"
		}

		// Check if the file exists in the embedded filesystem.
		f, err := sub.Open(strings.TrimPrefix(path, "/"))
		if err != nil {
			// If the file does not exist, and it's not a request for a static asset (like .js, .css),
			// then it's likely a client-side route. In that case, we should serve the index.html
			// and let the client-side router handle it.
			if os.IsNotExist(err) && !strings.Contains(path, ".") {
				path = "/index.html"
				f, err = sub.Open("index.html")
			} else {
				// It's a genuine 404 for a missing asset.
				http.NotFound(w, r)
				return
			}
		}

		if err != nil {
			// If we still have an error, something is wrong.
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		fileContent, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "Error reading file", http.StatusInternalServerError)
			return
		}

		// 替换为请求的url对应的domain，使其在反向代理或不同域名下也能正常工作。
		backendHost := os.Getenv("EXCALIDRAW_BACKEND_HOST")
		if backendHost == "" {
			backendHost = r.Host
		}
		modifiedContent := strings.ReplaceAll(string(fileContent), "firestore.googleapis.com", backendHost)
		modifiedContent = strings.ReplaceAll(modifiedContent, "ssl=!0", "ssl=0")
		modifiedContent = strings.ReplaceAll(modifiedContent, "ssl:!0", "ssl:0")

		// Set the correct Content-Type based on the file extension
		contentType := http.DetectContentType([]byte(modifiedContent))
		switch {
		case strings.HasSuffix(path, ".js"):
			contentType = "application/javascript"
		case strings.HasSuffix(path, ".html"):
			contentType = "text/html"
		case strings.HasSuffix(path, ".css"):
			contentType = "text/css"
		case strings.HasSuffix(path, ".wasm"):
			contentType = "application/wasm"
		case strings.HasSuffix(path, ".tsx"):
			contentType = "text/typescript"
		case strings.HasSuffix(path, ".png"):
			contentType = "image/png"
		case strings.HasSuffix(path, ".woff2"):
			contentType = "font/woff2"
		}

		// Serve the modified content
		w.Header().Set("Content-Type", contentType)
		_, err = w.Write([]byte(modifiedContent))
		if err != nil {
			http.Error(w, "Error serving file", http.StatusInternalServerError)
			return
		}
	}
}

func handleNotFound() http.HandlerFunc {
	ui := handleUI()
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") ||
			strings.HasPrefix(path, "/v0/") ||
			strings.HasPrefix(path, "/v1/") ||
			strings.HasPrefix(path, "/auth/") ||
			path == "/ws" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "API route not found"})
			return
		}
		ui(w, r)
	}
}

func setupRouter(store stores.Store) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Content-Length", "X-CSRF-Token", "Token", "session", "Origin", "Host", "Connection", "Accept-Encoding", "Accept-Language", "X-Requested-With", "x-goog-request-params", "x-firebase-appcheck", "x-firebase-client", "x-goog-api-client"},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	}))

	r.Route("/v1/projects/{project_id}/databases/{database_id}", func(r chi.Router) {
		r.Post("/documents:commit", firebase.HandleBatchCommit())
		r.Post("/documents:batchGet", firebase.HandleBatchGet())
	})

	r.Route("/api/v2", func(r chi.Router) {
		// Route for canvases, protected by JWT auth
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.AuthJWT)
			r.Route("/kv", func(r chi.Router) {
				r.Get("/", kv.HandleListCanvases(store))
				r.Route("/{key}", func(r chi.Router) {
					r.Get("/", kv.HandleGetCanvas(store))
					r.Put("/", kv.HandleSaveCanvas(store))
					r.Delete("/", kv.HandleDeleteCanvas(store))
					// 画布移组：PUT /api/v2/kv/{key}/workspace {workspaceId}
					r.Put("/workspace", kv.HandleMoveCanvasWorkspace(store))
				})
			})
			// Workspaces 画布分组管理（AstraDraw 迁移阶段 1）
			r.Route("/workspaces", func(r chi.Router) {
				r.Get("/", kv.HandleListWorkspaces(store))
				r.Post("/", kv.HandleCreateWorkspace(store))
				r.Route("/{id}", func(r chi.Router) {
					r.Put("/", kv.HandleUpdateWorkspace(store))
					r.Delete("/", kv.HandleDeleteWorkspace(store))
				})
			})
		})
		// AI chat proxy: NOT behind JWT — the client passes its own OpenAI key
		// (or the server's OPENAI_API_KEY is used server-side); the proxy just
		// forwards. JWT here would break the mixed-content path (http page ->
		// https AI target) where the browser can't reach the AI service directly.
		r.Route("/chat", func(r chi.Router) {
			r.Post("/completions", openai.HandleChatCompletion())
		})

		// Old routes for anonymous document sharing
		r.Post("/post/", documents.HandleCreate(store))
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", documents.HandleGet(store))
		})
	})

	r.Route("/auth", func(r chi.Router) {
		r.Get("/login", auth.HandleLogin)
		r.Get("/callback", auth.HandleCallback)
	})

	// mcp-excalidraw canvas identity: the CLI refuses to talk to a service
	// that does not identify itself. No pid is exposed so the CLI's `stop`
	// can never signal this process (we are the host app, not a disposable
	// canvas server). /api/files answers 200 with empty files so the CLI's
	// export never fails on the image-files lookup.
	mcStore, err := mcpcanvas.NewStore("")
	if err != nil {
		logrus.WithError(err).Fatal("failed to init mcpcanvas store")
	}
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHealth(w, map[string]interface{}{
			"status":         "ok",
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"elements_count": mcStore.Count(),
			"service":        mcpcanvas.ServiceName,
		})
	})
	r.Route("/api/elements", func(r chi.Router) {
		mcpcanvas.Routes(r, mcStore)
	})
	r.Route("/api/canvas", func(r chi.Router) {
		mcpcanvas.CanvasRoutes(r, mcStore)
	})
	r.Get("/ws", mcStore.ServeWS)
	r.Route("/api/snapshots", func(r chi.Router) {
		mcpcanvas.SnapshotRoutes(r, mcStore)
	})
	r.Route("/v0/b/{bucket}", func(r chi.Router) {
		mcpcanvas.StorageRoutes(r, mcStore)
	})
	r.Get("/api/files", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHealth(w, map[string]interface{}{"success": true, "files": map[string]interface{}{}})
	})
	r.Post("/api/files", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return r
}

func writeJSONHealth(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func setupSocketIO() *socketio.Server {
	opts := socketio.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(5000000)
	opts.SetPath("/socket.io")
	opts.SetAllowEIO3(true)
	opts.SetCors(&types.Cors{
		Origin:      "*",
		Credentials: true,
	})
	ioo := socketio.NewServer(nil, opts)
	sessions := newCollabSessionRegistry()

	ioo.On("connection", func(clients ...any) {
		socket := clients[0].(*socketio.Socket)
		me := socket.Id()
		myRoom := socketio.Room(me)
		socket.Emit("init-room")
		utils.Log().Printf("init room %v\n", myRoom)
		socket.On("join-room", func(datas ...any) {
			room, clientID, ok := parseJoinRoomData(datas)
			if !ok {
				utils.Log().Printf("Socket %v sent invalid join-room payload\n", me)
				return
			}

			previousRoom, replacedSocket := sessions.claim(me, room, clientID)
			if previousRoom != "" && previousRoom != room {
				socket.Leave(previousRoom)
			}
			utils.Log().Printf("Socket %v has joined %v\n", me, room)
			socket.Join(room)
			if replacedSocket != "" {
				utils.Log().Printf(
					"Socket %v replaces stale socket %v in room %v\n",
					me,
					replacedSocket,
					room,
				)
				ioo.In(socketio.Room(replacedSocket)).DisconnectSockets(true)
			}
			ioo.In(room).FetchSockets()(func(usersInRoom []*socketio.RemoteSocket, _ error) {
				newRoomUsers := socketIDsExcluding(usersInRoom, replacedSocket)
				if len(newRoomUsers) <= 1 {
					ioo.To(myRoom).Emit("first-in-room")
				} else {
					utils.Log().Printf("emit new user %v in room %v\n", me, room)
					socket.Broadcast().To(room).Emit("new-user", me)
				}

				utils.Log().Printf("room %v has users %v\n", room, newRoomUsers)
				ioo.In(room).Emit(
					"room-user-change",
					newRoomUsers,
				)

			})
		})
		socket.On("server-broadcast", func(datas ...any) {
			if len(datas) < 3 {
				return
			}
			roomID, ok := datas[0].(string)
			joinedRoom, joined := sessions.roomFor(me)
			if !ok || !joined || string(joinedRoom) != roomID {
				utils.Log().Printf("Socket %v attempted broadcast outside its room %v\n", me, roomID)
				return
			}
			utils.Log().Printf(" user %v sends update to room %v\n", me, roomID)
			socket.Broadcast().To(socketio.Room(roomID)).Emit("client-broadcast", datas[1], datas[2])
		})
		socket.On("server-volatile-broadcast", func(datas ...any) {
			if len(datas) < 3 {
				return
			}
			roomID, ok := datas[0].(string)
			if !ok {
				return
			}

			joinedRoom, joined := sessions.roomFor(me)
			isMainRoom := joined && string(joinedRoom) == roomID
			isFollowRoom := strings.HasPrefix(roomID, followRoomPrefix) &&
				roomID == followRoomPrefix+string(me)
			if !isMainRoom && !isFollowRoom {
				utils.Log().Printf("Socket %v attempted volatile broadcast outside its room %v\n", me, roomID)
				return
			}
			utils.Log().Printf(" user %v sends volatile update to room %v\n", me, roomID)
			socket.Volatile().Broadcast().To(socketio.Room(roomID)).Emit("client-broadcast", datas[1], datas[2])
		})

		socket.On("user-follow", func(datas ...any) {
			if len(datas) == 0 {
				return
			}
			targetID, action, ok := parseUserFollowPayload(datas[0])
			if !ok || targetID == me || !sessions.sameRoom(me, targetID) {
				utils.Log().Printf("Socket %v sent invalid user-follow payload\n", me)
				return
			}

			followRoom := socketio.Room(followRoomPrefix + string(targetID))
			switch action {
			case "FOLLOW":
				socket.Join(followRoom)
			case "UNFOLLOW":
				socket.Leave(followRoom)
			}
			emitFollowRoomChange(ioo, followRoom)
		})
		socket.On("disconnecting", func(datas ...any) {
			if room, ok := sessions.roomFor(me); ok {
				ioo.In(room).FetchSockets()(func(usersInRoom []*socketio.RemoteSocket, err error) {
					if err != nil {
						return
					}
					otherClients := socketIDsExcluding(usersInRoom, me)
					utils.Log().Printf("disconnecting %v from room %v\n", me, room)
					socket.Broadcast().To(room).Emit("room-user-change", otherClients)
				})
			}

			for _, currentRoom := range socket.Rooms().Keys() {
				if _, ok := followTargetFromRoom(currentRoom); ok {
					emitFollowRoomChange(ioo, currentRoom, me)
				}
			}
		})
		socket.On("disconnect", func(datas ...any) {
			sessions.release(me)
			utils.Log().Printf("Socket %v disconnected\n", me)
		})
	})
	return ioo

}

func waitForShutdown(ioo *socketio.Server) {
	exit := make(chan struct{})
	SignalC := make(chan os.Signal, 1)

	signal.Notify(SignalC, os.Interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range SignalC {
			switch s {
			case os.Interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				close(exit)
				return
			}
		}
	}()

	<-exit
	ioo.Close(nil)
	os.Exit(0)
	fmt.Println("Shutting down...")
	// TODO(patwie): Close other resources
	os.Exit(0)
}

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		logrus.Info("No .env file found")
	}

	listenAddress := flag.String("listen", ":3002", "The address to listen on.")
	logLevel := flag.String("loglevel", "info", "The log level (debug, info, warn, error).")
	flag.Parse()

	level, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		logrus.Fatalf("Invalid log level: %v", err)
	}
	logrus.SetLevel(level)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	auth.InitAuth()
	openai.Init()
	store := stores.GetStore()

	r := setupRouter(store)

	ioo := setupSocketIO()
	r.Mount("/socket.io/", ioo.ServeHandler(nil))
	r.NotFound(handleNotFound())

	logrus.WithField("addr", *listenAddress).Info("starting server")
	go func() {
		if err := http.ListenAndServe(*listenAddress, r); err != nil {
			logrus.WithField("event", "start server").Fatal(err)
		}
	}()

	logrus.Debug("Server is running in the background")
	waitForShutdown(ioo)
}
