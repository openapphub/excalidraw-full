package stores

import (
	"context"
	"excalidraw-complete/core"
	"path/filepath"
	"testing"
)

func TestGetStoreDefaultsToSQLiteWorkspaceShell(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "")
	t.Setenv("DATA_SOURCE_NAME", filepath.Join(t.TempDir(), "default.db"))

	store := GetStore()
	workspace, err := store.CreateShellWorkspace(
		context.Background(),
		"owner",
		"团队",
		"",
		core.WorkspaceShared,
	)
	if err != nil {
		t.Fatalf("默认存储必须支持 Workspace Shell: %v", err)
	}
	if workspace.ID == "" {
		t.Fatal("默认 SQLite 存储未创建 Workspace")
	}
}
