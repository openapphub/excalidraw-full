package firebase

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
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
)

var (
	savedItemsMu sync.RWMutex
	savedItems   = make(map[string]interface{})
)

func (body *BatchGetRequest) Bind(r *http.Request) (err error) {
	return nil
}
func (body *BatchCommitRequest) Bind(r *http.Request) (err error) {
	return nil
}
func HandleBatchCommit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := chi.URLParam(r, "project_id")
		databaseId := chi.URLParam(r, "database_id")
		_ = projectId
		_ = databaseId

		data := &BatchCommitRequest{}
		// Seems like requests is text/plain but content is json ...
		if err := render.DecodeJSON(r.Body, data); err != nil {
			fmt.Println(err)
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(data.Writes) == 0 {
			http.Error(w, "writes must not be empty", http.StatusBadRequest)
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

func HandleBatchGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		projectId := chi.URLParam(r, "project_id")
		databaseId := chi.URLParam(r, "database_id")
		fmt.Printf("Got %v and %v\n", projectId, databaseId)
		data := &BatchGetRequest{}

		// Seems like requests is text/plain but content is json ...
		if err := render.DecodeJSON(r.Body, data); err != nil {
			fmt.Println(err)
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(data.Documents) == 0 {
			http.Error(w, "documents must not be empty", http.StatusBadRequest)
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
