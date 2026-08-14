package filesystem

import (
	"bytes"
	"context"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/stores/shellstub"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/sirupsen/logrus"
)

// fsStore 落地 Document/Canvas/Workspace 存储；Workspace Shell 暂未支持，
// 嵌入 shellstub.Unsupported 满足统一接口。
type fsStore struct {
	shellstub.Unsupported
	basePath string
}

// NewStore creates a new filesystem-based store.
func NewStore(basePath string) *fsStore {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		log.Fatalf("failed to create base directory: %v", err)
	}
	return &fsStore{basePath: basePath}
}

// DocumentStore implementation for anonymous sharing
func (s *fsStore) FindID(ctx context.Context, id string) (*core.Document, error) {
	filePath := filepath.Join(s.basePath, id)
	log := logrus.WithField("document_id", id)

	log.WithField("file_path", filePath).Info("Retrieving document by ID")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.WithField("error", "document not found").Warn("Document with specified ID not found")
			return nil, fmt.Errorf("document with id %s not found", id)
		}
		log.WithError(err).Error("Failed to retrieve document")
		return nil, err
	}

	document := core.Document{
		Data: *bytes.NewBuffer(data),
	}

	log.Info("Document retrieved successfully")
	return &document, nil
}

func (s *fsStore) Create(ctx context.Context, document *core.Document) (string, error) {
	id := ulid.Make().String()
	filePath := filepath.Join(s.basePath, id)
	log := logrus.WithFields(logrus.Fields{
		"document_id": id,
		"file_path":   filePath,
	})
	log.Info("Creating new document")

	if err := os.WriteFile(filePath, document.Data.Bytes(), 0644); err != nil {
		log.WithError(err).Error("Failed to create document")
		return "", err
	}

	log.Info("Document created successfully")
	return id, nil
}

// CanvasStore implementation for user-owned canvases
func (s *fsStore) getUserCanvasPath(userID string) string {
	return filepath.Join(s.basePath, userID)
}

func (s *fsStore) List(ctx context.Context, userID string) ([]*core.Canvas, error) {
	userPath := s.getUserCanvasPath(userID)
	log := logrus.WithField("user_id", userID).WithField("path", userPath)

	files, err := os.ReadDir(userPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("User directory does not exist, returning empty list.")
			return []*core.Canvas{}, nil
		}
		log.WithError(err).Error("Failed to read user directory")
		return nil, err
	}

	canvases := make([]*core.Canvas, 0, len(files))
	for _, file := range files {
		if !file.IsDir() {
			filePath := filepath.Join(userPath, file.Name())
			fileInfo, err := file.Info()
			if err != nil {
				log.WithError(err).Warnf("Failed to get file info for %s, skipping", file.Name())
				continue
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				log.WithError(err).Warnf("Failed to read canvas file %s, skipping", file.Name())
				continue
			}

			var canvas core.Canvas
			if err := json.Unmarshal(data, &canvas); err != nil {
				log.WithError(err).Warnf("Failed to unmarshal canvas file %s, skipping", file.Name())
				continue
			}

			// For list view, we don't need the full data blob.
			// Also ensure we populate metadata from the filesystem.
			canvas.Data = nil
			canvas.UpdatedAt = fileInfo.ModTime()
			canvases = append(canvases, &canvas)
		}
	}

	log.Infof("Listed %d canvases", len(canvases))
	return canvases, nil
}

func (s *fsStore) Get(ctx context.Context, userID, id string) (*core.Canvas, error) {
	userPath := s.getUserCanvasPath(userID)
	filePath := filepath.Join(userPath, id)
	log := logrus.WithFields(logrus.Fields{"user_id": userID, "canvas_id": id, "path": filePath})

	// 关键修复：验证路径合法性
	absUserPath, err := filepath.Abs(userPath)
	if err != nil {
		return nil, err // or handle error appropriately
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err // or handle error appropriately
	}

	if !strings.HasPrefix(absFilePath, absUserPath) {
		return nil, fmt.Errorf("invalid path: access denied")
	}
	// 修复结束

	data, err := os.ReadFile(absFilePath) // 使用清理过的路径
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn("Canvas file not found")
			return nil, fmt.Errorf("canvas %s not found", id)
		}
		log.WithError(err).Error("Failed to read canvas file")
		return nil, err
	}

	info, err := os.Stat(filePath)
	if err != nil {
		log.WithError(err).Error("Failed to get file stats")
		return nil, err
	}

	var canvas core.Canvas
	if err := json.Unmarshal(data, &canvas); err != nil {
		log.WithError(err).Error("Failed to unmarshal canvas data")
		return nil, err
	}
	canvas.UpdatedAt = info.ModTime()

	log.Info("Canvas retrieved successfully")
	return &canvas, nil
}

func (s *fsStore) Save(ctx context.Context, canvas *core.Canvas) error {
	userPath := s.getUserCanvasPath(canvas.UserID)
	filePath := filepath.Join(userPath, canvas.ID)
	log := logrus.WithFields(logrus.Fields{"user_id": canvas.UserID, "canvas_id": canvas.ID, "path": filePath})

	if err := os.MkdirAll(userPath, 0755); err != nil {
		log.WithError(err).Error("Failed to create user directory")
		return err
	}

	// Set creation/update time before saving
	info, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		canvas.CreatedAt = time.Now()
	} else if err == nil {
		canvas.CreatedAt = info.ModTime() // This is not ideal, but filesystem doesn't store creation time easily.
	}
	canvas.UpdatedAt = time.Now()

	log.Info("Saving canvas")
	data, err := json.Marshal(canvas)
	if err != nil {
		log.WithError(err).Error("Failed to marshal canvas for saving")
		return err
	}

	err = os.WriteFile(filePath, data, 0644)
	if err != nil {
		log.WithError(err).Error("Failed to write canvas file")
		return err
	}

	return nil
}

func (s *fsStore) Delete(ctx context.Context, userID, id string) error {
	userPath := s.getUserCanvasPath(userID)
	filePath := filepath.Join(userPath, id)
	log := logrus.WithFields(logrus.Fields{"user_id": userID, "canvas_id": id, "path": filePath})

	err := os.Remove(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn("Canvas file not found for deletion, considered successful.")
			return nil // If it doesn't exist, the goal is achieved.
		}
		log.WithError(err).Error("Failed to delete canvas file")
		return err
	}

	log.Info("Canvas deleted successfully")
	return nil
}

// WorkspaceStore implementation for filesystem storage.
// Workspaces are persisted as a JSON file (workspaces.json) in the user's directory.
func (s *fsStore) getWorkspacesPath(userID string) string {
	return filepath.Join(s.getUserCanvasPath(userID), "workspaces.json")
}

func (s *fsStore) loadWorkspaces(userID string) (map[string]*core.Workspace, error) {
	path := s.getWorkspacesPath(userID)
	workspaces := make(map[string]*core.Workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return workspaces, nil
		}
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &workspaces); err != nil {
			return nil, err
		}
	}
	return workspaces, nil
}

func (s *fsStore) saveWorkspaces(userID string, workspaces map[string]*core.Workspace) error {
	userPath := s.getUserCanvasPath(userID)
	if err := os.MkdirAll(userPath, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(workspaces)
	if err != nil {
		return err
	}
	return os.WriteFile(s.getWorkspacesPath(userID), data, 0644)
}

func (s *fsStore) ListWorkspaces(ctx context.Context, userID string) ([]*core.Workspace, error) {
	workspaces, err := s.loadWorkspaces(userID)
	if err != nil {
		return nil, err
	}

	// 懒创建：首次访问（无任何 workspace）时补 default 分组
	if len(workspaces) == 0 {
		now := time.Now()
		workspaces[core.DefaultWorkspaceID] = &core.Workspace{
			ID:        core.DefaultWorkspaceID,
			Name:      "默认分组",
			Note:      "",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.saveWorkspaces(userID, workspaces); err != nil {
			return nil, err
		}
	}

	list := make([]*core.Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		list = append(list, ws)
	}
	return list, nil
}

func (s *fsStore) CreateWorkspace(ctx context.Context, userID, name, note string) (*core.Workspace, error) {
	workspaces, err := s.loadWorkspaces(userID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	ws := &core.Workspace{
		ID:        ulid.Make().String(),
		Name:      name,
		Note:      note,
		CreatedAt: now,
		UpdatedAt: now,
	}
	workspaces[ws.ID] = ws
	if err := s.saveWorkspaces(userID, workspaces); err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *fsStore) UpdateWorkspace(ctx context.Context, userID, id, name, note string) error {
	workspaces, err := s.loadWorkspaces(userID)
	if err != nil {
		return err
	}
	ws, ok := workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	ws.Name = name
	ws.Note = note
	ws.UpdatedAt = time.Now()
	return s.saveWorkspaces(userID, workspaces)
}

func (s *fsStore) DeleteWorkspace(ctx context.Context, userID, id string) error {
	if id == core.DefaultWorkspaceID {
		return core.ErrDeleteDefaultWorkspace
	}

	workspaces, err := s.loadWorkspaces(userID)
	if err != nil {
		return err
	}
	if _, ok := workspaces[id]; !ok {
		return fmt.Errorf("workspace not found")
	}

	// 删除前先把该组画布迁回 default（画布是 JSON 文件，逐个回写）
	canvases, err := s.List(ctx, userID)
	if err != nil {
		return err
	}
	for _, canvas := range canvases {
		if canvas.WorkspaceID == id {
			canvas.WorkspaceID = core.DefaultWorkspaceID
			if err := s.Save(ctx, canvas); err != nil {
				return err
			}
		}
	}

	delete(workspaces, id)
	return s.saveWorkspaces(userID, workspaces)
}

func (s *fsStore) MoveCanvasWorkspace(ctx context.Context, userID, canvasID, workspaceID string) error {
	workspaces, err := s.loadWorkspaces(userID)
	if err != nil {
		return err
	}
	if _, ok := workspaces[workspaceID]; !ok {
		return fmt.Errorf("workspace not found")
	}

	canvas, err := s.Get(ctx, userID, canvasID)
	if err != nil {
		return fmt.Errorf("canvas not found")
	}
	canvas.WorkspaceID = workspaceID
	return s.Save(ctx, canvas)
}
