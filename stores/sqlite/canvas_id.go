package sqlite

import "fmt"

// IndexedDB 画布 id 是 UUID（8-4-4-4-12）。Workspace scene / CreateScene 用 ULID，
// 旧「我的创作」用 nanoid。登录后若把 UUID upsert 进 SQLite，Workspace 会混入浏览器本地数据。
const indexedDBCanvasIDGlob = "????????-????-????-????-????????????"

func isIndexedDBCanvasID(id string) bool {
	if len(id) != 36 {
		return false
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func workspaceListedSceneSQL(idExpr string) string {
	return fmt.Sprintf(" AND %s NOT GLOB '%s'", idExpr, indexedDBCanvasIDGlob)
}
