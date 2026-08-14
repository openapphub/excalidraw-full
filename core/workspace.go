package core

import (
	"context"
	"errors"
	"time"
)

type (
	// Workspace represents a user-owned group of canvases.
	Workspace struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Note      string    `json:"note,omitempty"`
		CreatedAt time.Time `json:"createdAt"`
		UpdatedAt time.Time `json:"updatedAt"`
	}

	// WorkspaceStore defines the persistence layer for user-owned workspace
	// groups. All operations are scoped to a specific user.
	WorkspaceStore interface {
		// ListWorkspaces returns all workspaces owned by a user. When the user
		// has none (first visit), the default workspace is created lazily and
		// returned as a single-element list.
		ListWorkspaces(ctx context.Context, userID string) ([]*Workspace, error)

		// CreateWorkspace creates a new workspace and returns it with its ID
		// and timestamps filled in.
		CreateWorkspace(ctx context.Context, userID, name, note string) (*Workspace, error)

		// UpdateWorkspace renames a workspace and/or changes its note. Returns
		// an error if the workspace does not exist or belongs to another user.
		UpdateWorkspace(ctx context.Context, userID, id, name, note string) error

		// DeleteWorkspace removes a workspace. All canvases inside it are moved
		// back to the default workspace first. The default workspace itself
		// cannot be deleted and returns ErrDeleteDefaultWorkspace.
		DeleteWorkspace(ctx context.Context, userID, id string) error

		// MoveCanvasWorkspace moves a canvas into another workspace. Returns an
		// error if the canvas or the target workspace does not exist.
		MoveCanvasWorkspace(ctx context.Context, userID, canvasID, workspaceID string) error
	}
)

// DefaultWorkspaceID is the workspace every canvas belongs to unless the
// user explicitly moves it elsewhere.
const DefaultWorkspaceID = "default"

// ErrDeleteDefaultWorkspace is returned when attempting to delete the default
// workspace.
var ErrDeleteDefaultWorkspace = errors.New("cannot delete the default workspace")
