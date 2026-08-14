package firebase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func resetSavedItems() {
	savedItemsMu.Lock()
	savedItems = make(map[string]interface{})
	savedItemsMu.Unlock()
}

func performJSONRequest(t *testing.T, handler http.HandlerFunc, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encoded))
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestBatchHandlersRejectEmptyArrays(t *testing.T) {
	resetSavedItems()

	commit := performJSONRequest(t, HandleBatchCommit(), BatchCommitRequest{})
	if commit.Code != http.StatusBadRequest {
		t.Fatalf("空 writes 状态码 = %d，期望 %d", commit.Code, http.StatusBadRequest)
	}

	get := performJSONRequest(t, HandleBatchGet(), BatchGetRequest{})
	if get.Code != http.StatusBadRequest {
		t.Fatalf("空 documents 状态码 = %d，期望 %d", get.Code, http.StatusBadRequest)
	}
}

func TestBatchHandlersProcessAllEntries(t *testing.T) {
	resetSavedItems()

	writes := BatchCommitRequest{Writes: []WriteRequest{
		{Update: UpdateRequest{Name: "documents/first", Fields: map[string]interface{}{"value": "one"}}},
		{Update: UpdateRequest{Name: "documents/second", Fields: map[string]interface{}{"value": "two"}}},
	}}
	commit := performJSONRequest(t, HandleBatchCommit(), writes)
	if commit.Code != http.StatusOK {
		t.Fatalf("批量提交状态码 = %d，响应 = %s", commit.Code, commit.Body.String())
	}
	var commitResponse BatchCommitResponse
	if err := json.Unmarshal(commit.Body.Bytes(), &commitResponse); err != nil {
		t.Fatalf("解析批量提交响应失败: %v", err)
	}
	if len(commitResponse.WriteResults) != len(writes.Writes) {
		t.Fatalf("writeResults 数量 = %d，期望 %d", len(commitResponse.WriteResults), len(writes.Writes))
	}

	get := performJSONRequest(t, HandleBatchGet(), BatchGetRequest{Documents: []string{
		"documents/first",
		"documents/missing",
		"documents/second",
	}})
	if get.Code != http.StatusOK {
		t.Fatalf("批量读取状态码 = %d，响应 = %s", get.Code, get.Body.String())
	}
	var responses []struct {
		Found   *FoundInfoResponse `json:"found"`
		Missing string             `json:"missing"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &responses); err != nil {
		t.Fatalf("解析批量读取响应失败: %v", err)
	}
	if len(responses) != 3 {
		t.Fatalf("批量读取响应数量 = %d，期望 3", len(responses))
	}
	if responses[0].Found == nil || responses[0].Found.Name != "documents/first" {
		t.Fatalf("第一条响应不正确: %+v", responses[0])
	}
	if responses[1].Missing != "documents/missing" {
		t.Fatalf("第二条响应不正确: %+v", responses[1])
	}
	if responses[2].Found == nil || responses[2].Found.Name != "documents/second" {
		t.Fatalf("第三条响应不正确: %+v", responses[2])
	}
}

func TestBatchHandlersConcurrentAccess(t *testing.T) {
	resetSavedItems()

	const workers = 64
	var waitGroup sync.WaitGroup
	errors := make(chan error, workers)
	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			name := fmt.Sprintf("documents/%d", index)

			commit := performJSONRequest(t, HandleBatchCommit(), BatchCommitRequest{Writes: []WriteRequest{
				{Update: UpdateRequest{Name: name, Fields: map[string]interface{}{"value": index}}},
			}})
			if commit.Code != http.StatusOK {
				errors <- fmt.Errorf("提交 %s 返回 %d", name, commit.Code)
				return
			}

			get := performJSONRequest(t, HandleBatchGet(), BatchGetRequest{Documents: []string{name}})
			if get.Code != http.StatusOK {
				errors <- fmt.Errorf("读取 %s 返回 %d", name, get.Code)
			}
		}(i)
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	savedItemsMu.RLock()
	itemCount := len(savedItems)
	savedItemsMu.RUnlock()
	if itemCount != workers {
		t.Fatalf("保存条目数量 = %d，期望 %d", itemCount, workers)
	}
}
