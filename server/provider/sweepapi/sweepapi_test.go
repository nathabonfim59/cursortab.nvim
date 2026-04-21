package sweepapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursortab/assert"
	"cursortab/client/sweepapi"
	"cursortab/engine"
	"cursortab/types"

	"github.com/andybalholm/brotli"
)

func TestFormatRecentChanges(t *testing.T) {
	tests := []struct {
		name         string
		histories    []*types.FileDiffHistory
		wantEmpty    bool
		wantContains string
	}{
		{
			name:      "nil histories",
			histories: nil,
			wantEmpty: true,
		},
		{
			name:      "empty histories",
			histories: []*types.FileDiffHistory{},
			wantEmpty: true,
		},
		{
			name: "single history with entries",
			histories: []*types.FileDiffHistory{
				{
					FileName: "test.go",
					DiffHistory: []*types.DiffEntry{
						{Original: "old code", Updated: "new code"},
					},
				},
			},
			wantEmpty:    false,
			wantContains: "File: test.go:",
		},
		{
			name: "history with empty diff",
			histories: []*types.FileDiffHistory{
				{
					FileName:    "test.go",
					DiffHistory: []*types.DiffEntry{},
				},
			},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, resultHighRes := formatRecentChangesSplit(tt.histories)
			if tt.wantEmpty {
				assert.Equal(t, "", result, "should be empty")
				assert.Equal(t, "", resultHighRes, "high-res should be empty")
			} else {
				assert.True(t, len(result) > 0, "should not be empty")
				assert.True(t, len(resultHighRes) > 0, "high-res should not be empty")
				if tt.wantContains != "" {
					assert.True(t, strings.Contains(result, tt.wantContains), "should contain expected string")
				}
			}
		})
	}
}

func TestFormatDiagnostics(t *testing.T) {
	tests := []struct {
		name         string
		linterErrors *types.Diagnostics
		wantLen      int
	}{
		{
			name:         "nil linter errors",
			linterErrors: nil,
			wantLen:      0,
		},
		{
			name: "empty errors slice",
			linterErrors: &types.Diagnostics{
				FilePath: "test.go",
				Items:    []*types.Diagnostic{},
			},
			wantLen: 0,
		},
		{
			name: "single error",
			linterErrors: &types.Diagnostics{
				FilePath: "test.go",
				Items: []*types.Diagnostic{
					{
						Message: "undefined variable",
						Source:  "go",
						Range:   &types.CursorRange{StartLine: 10},
					},
				},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{limits: engine.DefaultContextLimits()}
			result := p.buildEditorDiagnostics(tt.linterErrors)
			assert.Equal(t, tt.wantLen, len(result), "diagnostics count")
		})
	}
}

func TestProviderGetCompletion(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and decompress request
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		decompressed, _ := io.ReadAll(brotliReader)

		var req sweepapi.AutocompleteRequest
		json.Unmarshal(decompressed, &req)

		// Return a simple completion
		resp := sweepapi.AutocompleteResponse{
			StartIndex: 6,                  // Start of "world"
			EndIndex:   11,                 // End of "world"
			Completion: "universe is vast", // Replace "world" with longer text
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
		APIKey:      "test-token",
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"hello world"},
		CursorRow: 1,
		CursorCol: 11,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Len(t, 1, resp.Completions, "completions")

	completion := resp.Completions[0]
	assert.Equal(t, 1, completion.StartLine, "StartLine")
	assert.Len(t, 1, completion.Lines, "lines")
	assert.Equal(t, "hello universe is vast", completion.Lines[0], "line content")
}

func TestProviderEmptyResponse(t *testing.T) {
	// Create a test server that returns empty completion
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and decompress to avoid errors
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		resp := sweepapi.AutocompleteResponse{
			StartIndex: 0,
			EndIndex:   0,
			Completion: "",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"hello"},
		CursorRow: 1,
		CursorCol: 5,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 0, len(resp.Completions), "completions count")
}

func TestProviderMultilineCompletion(t *testing.T) {
	// Create a test server that returns multiline completion
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		// text is "func main() {\n\n}" (byte 14 is the empty line)
		// Replace the empty line with implementation
		resp := sweepapi.AutocompleteResponse{
			StartIndex: 14,
			EndIndex:   15,
			Completion: "\tfmt.Println(\"hello\")\n",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"func main() {", "", "}"},
		CursorRow: 2,
		CursorCol: 0,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Len(t, 1, resp.Completions, "completions")

	completion := resp.Completions[0]
	assert.Equal(t, 2, completion.StartLine, "StartLine")
	// EndLineInc should be 2 (the original end line in the buffer being replaced)
	// The byte offset 15 is in line 2, so EndLineInc should be 2
	assert.Equal(t, 2, completion.EndLineInc, "EndLineInc")
}

func TestGetCompletionReturnsMetricsInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend/track_autocomplete_metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Completion endpoint
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		resp := sweepapi.AutocompleteResponse{
			AutocompleteID: "test-completion-id",
			StartIndex:     0,
			EndIndex:       5,
			Completion:     "hello world",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"hello"},
		CursorRow: 1,
		CursorCol: 5,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")

	// Verify MetricsInfo is returned
	assert.NotNil(t, resp.MetricsInfo, "MetricsInfo should be set")
	assert.Equal(t, "test-completion-id", resp.MetricsInfo.ID, "MetricsInfo.ID")
	assert.True(t, resp.MetricsInfo.Additions > 0, "MetricsInfo.Additions should be positive")
	assert.True(t, resp.MetricsInfo.Deletions > 0, "MetricsInfo.Deletions should be positive")
}

func TestGetCompletionIncludesFileChunks(t *testing.T) {
	var receivedReq sweepapi.AutocompleteRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend/track_autocomplete_metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Capture the request
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		decompressed, _ := io.ReadAll(brotliReader)
		json.Unmarshal(decompressed, &receivedReq)

		resp := sweepapi.AutocompleteResponse{
			AutocompleteID: "test-id",
			StartIndex:     0,
			EndIndex:       5,
			Completion:     "hello world",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "main.go",
		Lines:     []string{"package main", "", "func main() {}"},
		CursorRow: 3,
		CursorCol: 14,
		RecentBufferSnapshots: []*types.RecentBufferSnapshot{
			{
				FilePath:    "/project/utils.go",
				Lines:       []string{"package utils", "", "func Helper() {}"},
				TimestampMs: 1234567890,
			},
			{
				FilePath:    "/project/config.go",
				Lines:       []string{"package config", "var Debug = true"},
				TimestampMs: 1234567800,
			},
		},
	}

	_, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")

	// Verify FileChunks were included
	assert.Len(t, 2, receivedReq.FileChunks, "FileChunks count")

	chunk1 := receivedReq.FileChunks[0]
	assert.Equal(t, "/project/utils.go", chunk1.FilePath, "first chunk path")
	assert.True(t, strings.Contains(chunk1.Content, "package utils"), "first chunk content")
	assert.NotNil(t, chunk1.Timestamp, "first chunk timestamp should be set")
	assert.Equal(t, uint64(1234567890), *chunk1.Timestamp, "first chunk timestamp value")

	chunk2 := receivedReq.FileChunks[1]
	assert.Equal(t, "/project/config.go", chunk2.FilePath, "second chunk path")
}

func TestGetCompletionIncludesUserActions(t *testing.T) {
	var receivedReq sweepapi.AutocompleteRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend/track_autocomplete_metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}

		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		decompressed, _ := io.ReadAll(brotliReader)
		json.Unmarshal(decompressed, &receivedReq)

		resp := sweepapi.AutocompleteResponse{
			AutocompleteID: "test-id",
			StartIndex:     0,
			EndIndex:       5,
			Completion:     "hello",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "main.go",
		Lines:     []string{"func main() {}"},
		CursorRow: 1,
		CursorCol: 14,
		UserActions: []*types.UserAction{
			{
				ActionType:  types.ActionInsertChar,
				FilePath:    "main.go",
				LineNumber:  1,
				Offset:      14,
				TimestampMs: 1000,
			},
			{
				ActionType:  types.ActionCursorMovement,
				FilePath:    "main.go",
				LineNumber:  1,
				Offset:      10,
				TimestampMs: 900,
			},
			{
				ActionType:  types.ActionDeleteChar,
				FilePath:    "main.go",
				LineNumber:  1,
				Offset:      13,
				TimestampMs: 950,
			},
		},
	}

	_, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")

	// Verify UserActions were included
	assert.Len(t, 3, receivedReq.RecentUserActions, "UserActions count")

	action1 := receivedReq.RecentUserActions[0]
	assert.Equal(t, "INSERT_CHAR", action1.ActionType, "first action type")
	assert.Equal(t, "main.go", action1.FilePath, "first action path")
	assert.Equal(t, 1, action1.LineNumber, "first action line")
	assert.Equal(t, 14, action1.Offset, "first action offset")
	assert.Equal(t, int64(1000), action1.Timestamp, "first action timestamp")

	action2 := receivedReq.RecentUserActions[1]
	assert.Equal(t, "CURSOR_MOVEMENT", action2.ActionType, "second action type")

	action3 := receivedReq.RecentUserActions[2]
	assert.Equal(t, "DELETE_CHAR", action3.ActionType, "third action type")
}

func TestGetCompletionWithEmptyContextFields(t *testing.T) {
	var receivedJSON map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend/track_autocomplete_metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}

		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		decompressed, _ := io.ReadAll(brotliReader)
		json.Unmarshal(decompressed, &receivedJSON)

		resp := sweepapi.AutocompleteResponse{
			Completion: "",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:              "main.go",
		Lines:                 []string{"hello"},
		CursorRow:             1,
		CursorCol:             5,
		RecentBufferSnapshots: nil, // Explicitly nil
		UserActions:           nil, // Explicitly nil
	}

	_, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")

	// Verify empty arrays are sent in JSON (not null)
	fileChunks, ok := receivedJSON["file_chunks"].([]any)
	assert.True(t, ok, "FileChunks should be an array in JSON")
	assert.Equal(t, 0, len(fileChunks), "FileChunks should be empty")

	userActions, ok := receivedJSON["recent_user_actions"].([]any)
	assert.True(t, ok, "UserActions should be an array in JSON")
	assert.Equal(t, 0, len(userActions), "UserActions should be empty")
}
