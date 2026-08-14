package stores

import (
	"excalidraw-complete/core"
	"excalidraw-complete/stores/aws"
	"excalidraw-complete/stores/filesystem"
	"excalidraw-complete/stores/memory"
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
	case "filesystem":
		basePath := os.Getenv("LOCAL_STORAGE_PATH")
		if basePath == "" {
			basePath = "./data" // Default path
		}
		storageField["basePath"] = basePath
		store = filesystem.NewStore(basePath)
	case "sqlite":
		dataSourceName := os.Getenv("DATA_SOURCE_NAME")
		if dataSourceName == "" {
			dataSourceName = "excalidraw.db" // Default filename
		}
		storageField["dataSourceName"] = dataSourceName
		store = sqlite.NewStore(dataSourceName)
	case "s3":
		bucketName := os.Getenv("S3_BUCKET_NAME")
		if bucketName == "" {
			logrus.Fatal("S3_BUCKET_NAME environment variable must be set for s3 storage type")
		}
		storageField["bucketName"] = bucketName
		store = aws.NewStore(bucketName)
	default:
		store = memory.NewStore()
		storageField["storageType"] = "in-memory"
	}
	logrus.WithFields(storageField).Info("Use storage")
	return store
}
