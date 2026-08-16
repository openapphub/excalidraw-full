package stores

import (
	"excalidraw-complete/core"
	"excalidraw-complete/stores/sqlite"
	"os"

	"github.com/sirupsen/logrus"
)

// Store is a union interface that includes all store types.
type Store interface {
	core.DocumentStore
	core.CanvasStore
	core.WorkspaceStore
	core.ShellStore
	core.CommentStore
	core.LocalAuthStore
}

func GetStore() Store {
	storageType := os.Getenv("STORAGE_TYPE")
	var store Store

	storageField := logrus.Fields{
		"storageType": storageType,
	}

	switch storageType {
	case "", "sqlite":
		dataSourceName := os.Getenv("DATA_SOURCE_NAME")
		if dataSourceName == "" {
			dataSourceName = "excalidraw.db" // Default filename
		}
		storageField["storageType"] = "sqlite"
		storageField["dataSourceName"] = dataSourceName
		store = sqlite.NewStore(dataSourceName)
	default:
		logrus.Fatalf(
			"STORAGE_TYPE=%q does not support Workspace Shell ACL, transactions, and scene locks; use STORAGE_TYPE=sqlite",
			storageType,
		)
		return nil
	}
	logrus.WithFields(storageField).Info("Use storage")
	return store
}
