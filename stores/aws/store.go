package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/oklog/ulid/v2"
)

type s3Store struct {
	s3Client *s3.Client
	bucket   string
}

// NewStore creates a new S3-based store.
func NewStore(bucketName string) *s3Store {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("unable to load SDK config, %v", err)
	}

	s3Client := s3.NewFromConfig(cfg)

	return &s3Store{
		s3Client: s3Client,
		bucket:   bucketName,
	}
}

// DocumentStore implementation for anonymous sharing
func (s *s3Store) FindID(ctx context.Context, id string) (*core.Document, error) {
	resp, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(id),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get document with id %s: %v", id, err)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read document data: %v", err)
	}

	document := core.Document{
		Data: *bytes.NewBuffer(data),
	}

	return &document, nil
}

func (s *s3Store) Create(ctx context.Context, document *core.Document) (string, error) {
	id := ulid.Make().String()

	_, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(id),
		Body:   bytes.NewReader(document.Data.Bytes()),
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload document: %v", err)
	}

	return id, nil
}

// CanvasStore implementation for user-owned canvases
func (s *s3Store) getCanvasKey(userID, canvasID string) (string, error) {
	// Sanitize canvasID to prevent path traversal attacks.
	// It should be a simple name, not a path.
	if path.Base(canvasID) != canvasID {
		return "", fmt.Errorf("invalid canvas id: must not be a path")
	}
	if canvasID == "" || canvasID == "." || canvasID == ".." {
		return "", fmt.Errorf("invalid canvas id: must not be empty or a dot directory")
	}
	return path.Join(userID, canvasID), nil
}

func (s *s3Store) List(ctx context.Context, userID string) ([]*core.Canvas, error) {
	prefix := userID + "/"
	output, err := s.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list canvases for user %s: %v", userID, err)
	}

	canvases := make([]*core.Canvas, 0, len(output.Contents))
	for _, object := range output.Contents {
		resp, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    object.Key,
		})
		if err != nil {
			log.Printf("warn: failed to get object %s: %v", *object.Key, err)
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("warn: failed to read object body %s: %v", *object.Key, err)
			continue
		}

		var canvas core.Canvas
		if err := json.Unmarshal(data, &canvas); err != nil {
			log.Printf("warn: failed to unmarshal canvas %s: %v", *object.Key, err)
			continue
		}

		// For list view, we don't need the full data blob.
		canvas.Data = nil
		canvases = append(canvases, &canvas)
	}

	return canvases, nil
}

func (s *s3Store) Get(ctx context.Context, userID, id string) (*core.Canvas, error) {
	key, err := s.getCanvasKey(userID, id)
	if err != nil {
		return nil, err
	}
	resp, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// A specific check for NoSuchKey can be useful here.
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, fmt.Errorf("canvas not found")
		}
		return nil, fmt.Errorf("failed to get canvas %s: %v", id, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read canvas data: %v", err)
	}

	var canvas core.Canvas
	if err := json.Unmarshal(data, &canvas); err != nil {
		return nil, fmt.Errorf("failed to unmarshal canvas data: %v", err)
	}

	return &canvas, nil
}

func (s *s3Store) Save(ctx context.Context, canvas *core.Canvas) error {
	key, err := s.getCanvasKey(canvas.UserID, canvas.ID)
	if err != nil {
		return err
	}

	// Preserve CreatedAt on update
	if canvas.CreatedAt.IsZero() {
		existing, err := s.Get(ctx, canvas.UserID, canvas.ID)
		if err == nil && existing != nil {
			canvas.CreatedAt = existing.CreatedAt
		} else {
			canvas.CreatedAt = time.Now()
		}
	}
	canvas.UpdatedAt = time.Now()

	data, err := json.Marshal(canvas)
	if err != nil {
		return fmt.Errorf("failed to marshal canvas: %v", err)
	}

	_, err = s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("failed to save canvas %s: %v", canvas.ID, err)
	}
	return nil
}

func (s *s3Store) Delete(ctx context.Context, userID, id string) error {
	key, err := s.getCanvasKey(userID, id)
	if err != nil {
		return err
	}
	_, err = s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete canvas %s: %v", id, err)
	}
	return nil
}

// WorkspaceStore implementation for S3 storage.
// Workspaces are persisted as a single JSON object (workspaces.json) under the user's prefix.
func (s *s3Store) loadWorkspaces(ctx context.Context, userID string) (map[string]*core.Workspace, error) {
	workspaces := make(map[string]*core.Workspace)
	resp, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(path.Join(userID, "workspaces.json")),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return workspaces, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &workspaces); err != nil {
			return nil, err
		}
	}
	return workspaces, nil
}

func (s *s3Store) saveWorkspaces(ctx context.Context, userID string, workspaces map[string]*core.Workspace) error {
	data, err := json.Marshal(workspaces)
	if err != nil {
		return err
	}
	_, err = s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(path.Join(userID, "workspaces.json")),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *s3Store) ListWorkspaces(ctx context.Context, userID string) ([]*core.Workspace, error) {
	workspaces, err := s.loadWorkspaces(ctx, userID)
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
		if err := s.saveWorkspaces(ctx, userID, workspaces); err != nil {
			return nil, err
		}
	}

	list := make([]*core.Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		list = append(list, ws)
	}
	return list, nil
}

func (s *s3Store) CreateWorkspace(ctx context.Context, userID, name, note string) (*core.Workspace, error) {
	workspaces, err := s.loadWorkspaces(ctx, userID)
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
	if err := s.saveWorkspaces(ctx, userID, workspaces); err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *s3Store) UpdateWorkspace(ctx context.Context, userID, id, name, note string) error {
	workspaces, err := s.loadWorkspaces(ctx, userID)
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
	return s.saveWorkspaces(ctx, userID, workspaces)
}

func (s *s3Store) DeleteWorkspace(ctx context.Context, userID, id string) error {
	if id == core.DefaultWorkspaceID {
		return core.ErrDeleteDefaultWorkspace
	}

	workspaces, err := s.loadWorkspaces(ctx, userID)
	if err != nil {
		return err
	}
	if _, ok := workspaces[id]; !ok {
		return fmt.Errorf("workspace not found")
	}

	// 删除前先把该组画布迁回 default（画布是 JSON 对象，逐个回写）
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
	return s.saveWorkspaces(ctx, userID, workspaces)
}

func (s *s3Store) MoveCanvasWorkspace(ctx context.Context, userID, canvasID, workspaceID string) error {
	workspaces, err := s.loadWorkspaces(ctx, userID)
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
