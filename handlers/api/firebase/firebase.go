package firebase

import (
	"context"
	"encoding/base64"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/api/roomaccess"
	"excalidraw-complete/stores"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"github.com/sirupsen/logrus"
)

type (
	BatchGetRequest struct {
		Documents []string `json:"documents"`
	}
	BatchGetEmptyResponse struct {
		Missing  string `json:"missing"`
		ReadTime string `json:"readTime"`
	}

	FoundInfoResponse struct {
		Name       string      `json:"name"`
		Fields     interface{} `json:"fields"`
		CreateTime string      `json:"createTime"`
		UpdateTime string      `json:"updateTime"`
	}
	BatchGetExistsResponse struct {
		Found    FoundInfoResponse `json:"found"`
		ReadTime string            `json:"readTime"`
	}

	UpdateRequest struct {
		Name   string      `json:"name"`
		Fields interface{} `json:"fields"`
	}
	WriteRequest struct {
		Update UpdateRequest `json:"update"`
	}
	BatchCommitRequest struct {
		Writes []WriteRequest `json:"writes"`
	}

	WriteResult struct {
		UpdateTime string `json:"updateTime"`
	}
	BatchCommitResponse struct {
		WriteResults []WriteResult `json:"writeResults"`
		CommitTime   string        `json:"commitTime"`
	}

	// RoomSnapshot 是自建版临时协作房间的加密快照。服务端只保存密文，
	// revision 用于客户端乐观并发重试，避免两个客户端互相覆盖快照。
	RoomSnapshot struct {
		Revision     uint64 `json:"revision"`
		SceneVersion int64  `json:"sceneVersion"`
		Ciphertext   string `json:"ciphertext"`
		IV           string `json:"iv"`
		UpdatedAt    string `json:"updatedAt"`
	}
	RoomSnapshotWriteRequest struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		SceneVersion     int64  `json:"sceneVersion"`
		Ciphertext       string `json:"ciphertext"`
		IV               string `json:"iv"`
	}
	roomSnapshotEntry struct {
		snapshot  RoomSnapshot
		storedAt  time.Time
		byteCount int64
	}
)

var (
	savedItemsMu      sync.RWMutex
	savedItems        = make(map[string]interface{})
	roomSnapshots     = make(map[string]roomSnapshotEntry)
	roomSnapshotBytes int64
)

const (
	maxRoomSnapshotRequestBytes = 8 << 20
	maxRoomSnapshotBytes        = 128 << 20
	roomSnapshotTTL             = 24 * time.Hour
)

func pruneRoomSnapshotsLocked(now time.Time) {
	for roomID, entry := range roomSnapshots {
		if now.Sub(entry.storedAt) < roomSnapshotTTL {
			continue
		}
		delete(roomSnapshots, roomID)
		roomSnapshotBytes -= entry.byteCount
	}
}

func reserveRoomSnapshotSpaceLocked(required int64, protectedRoomID string) {
	for roomSnapshotBytes+required > maxRoomSnapshotBytes {
		oldestRoomID := ""
		var oldest roomSnapshotEntry
		for roomID, entry := range roomSnapshots {
			if roomID == protectedRoomID {
				continue
			}
			if oldestRoomID == "" || entry.storedAt.Before(oldest.storedAt) {
				oldestRoomID = roomID
				oldest = entry
			}
		}
		if oldestRoomID == "" {
			return
		}
		delete(roomSnapshots, oldestRoomID)
		roomSnapshotBytes -= oldest.byteCount
	}
}

func (body *BatchGetRequest) Bind(r *http.Request) (err error) {
	return nil
}
func (body *BatchCommitRequest) Bind(r *http.Request) (err error) {
	return nil
}
func firestoreStore(values []stores.Store) stores.Store {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

type sceneContentWriteChecker interface {
	CheckSceneContentWrite(ctx context.Context, userID, sceneID string) error
}

func sceneIDFromDocumentName(name string) (string, bool) {
	const marker = "/documents/scenes/"
	index := strings.Index(name, marker)
	if index < 0 {
		if strings.HasPrefix(name, "documents/scenes/") {
			index = -1
			markerless := strings.TrimPrefix(name, "documents/scenes/")
			if markerless != "" && !strings.Contains(markerless, "/") {
				return markerless, true
			}
		}
		return "", false
	}
	sceneID := name[index+len(marker):]
	if sceneID == "" || strings.Contains(sceneID, "/") {
		return "", false
	}
	return sceneID, true
}

func authorizeDocuments(ctx context.Context, store stores.Store, token, clientID string, names []string, requireWrite bool) error {
	ctx = core.WithSceneClientID(ctx, strings.TrimSpace(clientID))
	for _, name := range names {
		sceneID, ok := sceneIDFromDocumentName(name)
		if !ok {
			if store != nil {
				return core.ErrForbidden
			}
			continue
		}
		access, err := roomaccess.Authorize(ctx, store, sceneID, token)
		if err != nil {
			return err
		}
		if access.WorkspaceScene && requireWrite && !access.CanEdit {
			return core.ErrForbidden
		}
		if access.WorkspaceScene && requireWrite {
			checker, ok := store.(sceneContentWriteChecker)
			if !ok {
				return core.ErrForbidden
			}
			if err := checker.CheckSceneContentWrite(ctx, access.UserID, sceneID); err != nil {
				return err
			}
		}
	}
	return nil
}

func authorizeRoomSnapshot(r *http.Request, store stores.Store, roomID string, requireWrite bool) error {
	return authorizeDocuments(
		r.Context(),
		store,
		roomaccess.BearerToken(r),
		r.Header.Get("X-Scene-Client-ID"),
		[]string{"documents/scenes/" + roomID},
		requireWrite,
	)
}

func writeAuthorizationError(w http.ResponseWriter, err error) {
	var lockErr *core.SceneLockError
	if errors.As(err, &lockErr) {
		http.Error(w, lockErr.Error(), http.StatusConflict)
		return
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
}

// HandleRoomSnapshotGet 绕开 Firebase WebChannel，直接读取自建服务保存的
// 端到端加密房间快照。匿名房间与 Workspace Scene 共用同一套服务端 ACL。
func HandleRoomSnapshotGet(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		roomID := strings.TrimSpace(chi.URLParam(r, "room_id"))
		if roomID == "" {
			http.Error(w, "room id is required", http.StatusBadRequest)
			return
		}
		if err := authorizeRoomSnapshot(r, store, roomID, false); err != nil {
			writeAuthorizationError(w, err)
			return
		}

		now := time.Now()
		savedItemsMu.Lock()
		pruneRoomSnapshotsLocked(now)
		entry, ok := roomSnapshots[roomID]
		savedItemsMu.Unlock()
		if !ok {
			http.Error(w, "room snapshot not found", http.StatusNotFound)
			return
		}

		render.JSON(w, r, entry.snapshot)
	}
}

// HandleRoomSnapshotPut 以 revision 做原子 compare-and-swap。冲突时返回
// 当前快照，客户端重新解密、合并后重试，避免最后写入者覆盖并发更新。
func HandleRoomSnapshotPut(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		roomID := strings.TrimSpace(chi.URLParam(r, "room_id"))
		if roomID == "" {
			http.Error(w, "room id is required", http.StatusBadRequest)
			return
		}
		if err := authorizeRoomSnapshot(r, store, roomID, true); err != nil {
			writeAuthorizationError(w, err)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRoomSnapshotRequestBytes)
		request := &RoomSnapshotWriteRequest{}
		if err := render.DecodeJSON(r.Body, request); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if request.SceneVersion < 0 || request.Ciphertext == "" || request.IV == "" {
			http.Error(w, "invalid room snapshot", http.StatusBadRequest)
			return
		}
		if _, err := base64.StdEncoding.DecodeString(request.Ciphertext); err != nil {
			http.Error(w, "invalid ciphertext", http.StatusBadRequest)
			return
		}
		if iv, err := base64.StdEncoding.DecodeString(request.IV); err != nil || len(iv) != 12 {
			http.Error(w, "invalid iv", http.StatusBadRequest)
			return
		}

		now := time.Now()
		savedItemsMu.Lock()
		pruneRoomSnapshotsLocked(now)
		currentEntry, exists := roomSnapshots[roomID]
		currentRevision := uint64(0)
		if exists {
			currentRevision = currentEntry.snapshot.Revision
		}
		if request.ExpectedRevision != currentRevision {
			savedItemsMu.Unlock()
			render.Status(r, http.StatusConflict)
			render.JSON(w, r, currentEntry.snapshot)
			return
		}
		snapshot := RoomSnapshot{
			Revision:     currentRevision + 1,
			SceneVersion: request.SceneVersion,
			Ciphertext:   request.Ciphertext,
			IV:           request.IV,
			UpdatedAt:    now.UTC().Format(time.RFC3339Nano),
		}
		byteCount := int64(len(request.Ciphertext) + len(request.IV))
		if exists {
			roomSnapshotBytes -= currentEntry.byteCount
		}
		reserveRoomSnapshotSpaceLocked(byteCount, roomID)
		roomSnapshots[roomID] = roomSnapshotEntry{
			snapshot:  snapshot,
			storedAt:  now,
			byteCount: byteCount,
		}
		roomSnapshotBytes += byteCount
		savedItemsMu.Unlock()

		render.JSON(w, r, snapshot)
	}
}

func HandleBatchCommit(store ...stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := chi.URLParam(r, "project_id")
		databaseId := chi.URLParam(r, "database_id")
		_ = projectId
		_ = databaseId

		data := &BatchCommitRequest{}
		// Seems like requests is text/plain but content is json ...
		if err := render.DecodeJSON(r.Body, data); err != nil {
			logrus.WithError(err).Warn("firebase: invalid commit body")
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(data.Writes) == 0 {
			http.Error(w, "writes must not be empty", http.StatusBadRequest)
			return
		}
		documentNames := make([]string, 0, len(data.Writes))
		for _, write := range data.Writes {
			documentNames = append(documentNames, write.Update.Name)
		}
		if err := authorizeDocuments(r.Context(), firestoreStore(store), roomaccess.BearerToken(r), r.Header.Get("X-Scene-Client-ID"), documentNames, true); err != nil {
			writeAuthorizationError(w, err)
			return
		}

		now := time.Now().UTC().Format(time.RFC3339)
		writeResults := make([]WriteResult, len(data.Writes))
		savedItemsMu.Lock()
		for i, write := range data.Writes {
			savedItems[write.Update.Name] = write.Update.Fields
			writeResults[i] = WriteResult{UpdateTime: now}
		}
		savedItemsMu.Unlock()

		render.Status(r, http.StatusOK)
		render.JSON(w, r, BatchCommitResponse{
			CommitTime:   now,
			WriteResults: writeResults,
		})
	}
}

func HandleBatchGet(store ...stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		projectId := chi.URLParam(r, "project_id")
		databaseId := chi.URLParam(r, "database_id")
		logrus.WithFields(logrus.Fields{"projectId": projectId, "databaseId": databaseId}).Debug("firebase: batchGet")
		data := &BatchGetRequest{}

		// Seems like requests is text/plain but content is json ...
		if err := render.DecodeJSON(r.Body, data); err != nil {
			logrus.WithError(err).Warn("firebase: invalid batchGet body")
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(data.Documents) == 0 {
			http.Error(w, "documents must not be empty", http.StatusBadRequest)
			return
		}
		if err := authorizeDocuments(r.Context(), firestoreStore(store), roomaccess.BearerToken(r), "", data.Documents, false); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		now := time.Now().UTC().Format(time.RFC3339)
		responses := make([]interface{}, 0, len(data.Documents))
		savedItemsMu.RLock()
		for _, key := range data.Documents {
			fields, ok := savedItems[key]
			if !ok {
				responses = append(responses, BatchGetEmptyResponse{
					Missing:  key,
					ReadTime: now,
				})
				continue
			}
			responses = append(responses, BatchGetExistsResponse{
				Found: FoundInfoResponse{
					Name:       key,
					Fields:     fields,
					CreateTime: now,
					UpdateTime: now,
				},
				ReadTime: now,
			})
		}
		savedItemsMu.RUnlock()

		render.Status(r, http.StatusOK)
		render.JSON(w, r, responses)
	}
}

// HandleListenChannel 接住 SDK 改写 host 后打到本机的 Firestore Listen。
// 以前这条路 404，客户端会狂重试，协作体感变慢。这里返回 200 空通道结束。
func HandleListenChannel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}\n"))
	}
}
