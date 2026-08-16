package workspace

import (
	"context"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/middleware"
	"excalidraw-complete/stores/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestGlobalSearchOnlyReturnsAccessibleWorkspaceData(t *testing.T) {
	store := sqlite.NewStore(filepath.Join(t.TempDir(), "global-search.db"))
	ctx := context.Background()
	const userID = "search-user"
	const outsiderID = "search-outsider"

	accessibleWorkspace, err := store.CreateShellWorkspace(ctx, userID, "Needle Workspace", "needle-workspace", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	accessibleCollection, err := store.CreateCollection(ctx, userID, accessibleWorkspace.ID, "Needle Collection", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	accessibleScene, err := store.CreateScene(ctx, userID, "Needle Scene", nil, []byte(`{"elements":[]}`), &accessibleCollection.ID)
	if err != nil {
		t.Fatal(err)
	}

	inaccessibleWorkspace, err := store.CreateShellWorkspace(ctx, outsiderID, "Needle Hidden Workspace", "needle-hidden", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	inaccessibleCollection, err := store.CreateCollection(ctx, outsiderID, inaccessibleWorkspace.ID, "Needle Hidden Collection", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateScene(ctx, outsiderID, "Needle Hidden Scene", nil, []byte(`{"elements":[]}`), &inaccessibleCollection.ID); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v2/search/global?q=needle&limit=10", nil)
	claims := &auth.AppClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: userID}}
	request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsContextKey, claims))
	response := httptest.NewRecorder()
	HandleGlobalSearch(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("全局搜索状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}

	var result struct {
		Collections []globalSearchCollectionResult `json:"collections"`
		Scenes      []globalSearchSceneResult      `json:"scenes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Collections) != 1 || result.Collections[0].ID != accessibleCollection.ID {
		t.Fatalf("集合搜索结果 = %+v，期望仅返回有权集合", result.Collections)
	}
	if len(result.Scenes) != 1 || result.Scenes[0].ID != accessibleScene.ID {
		t.Fatalf("场景搜索结果 = %+v，期望仅返回有权场景", result.Scenes)
	}
}

func TestGlobalSearchRejectsInvalidLimit(t *testing.T) {
	store := sqlite.NewStore(filepath.Join(t.TempDir(), "global-search-limit.db"))
	request := httptest.NewRequest(http.MethodGet, "/api/v2/search/global?limit=invalid", nil)
	claims := &auth.AppClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "search-user"}}
	request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsContextKey, claims))
	response := httptest.NewRecorder()

	HandleGlobalSearch(store).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("非法 limit 状态码 = %d，期望 400", response.Code)
	}
}
