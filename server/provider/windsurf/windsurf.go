// Package windsurf implements the Windsurf completion provider.
//
// This provider communicates with the Windsurf local language server
// over HTTP. The port is assigned randomly at runtime when the Neovim Codeium
// extension starts; we discover it via lua/cursortab/bridge.lua which probes the
// extension's internal state (codeium.s.port, codeium.s.healthy, api_key).
//
// The server exposes a JSON API endpoint:
//
//	POST http://127.0.0.1:<port>/exa.language_server_pb.LanguageServerService/GetCompletions
//
// Example request:
//
//	{
//	  "metadata": {
//	    "api_key": "<api_key>",
//	    "ide_name": "neovim",
//	    "ide_version": "0.10.0",
//	    "extension_name": "neovim",
//	    "extension_version": "1.20.9",
//	    "request_id": 1
//	  },
//	  "editor_options": { "tab_size": 4, "insert_spaces": true },
//	  "document": {
//	    "text": "package main\n\nfunc main() {\n\tfmt.Println(",
//	    "editor_language": "go",
//	    "language": 9,
//	    "cursor_position": { "row": 3, "col": 13 },
//	    "absolute_uri": "file:///tmp/main.go",
//	    "workspace_uri": "file:///tmp",
//	    "line_ending": "\n"
//	  }
//	}
//
// Example response:
//
//	{
//	  "state": { "state": "CODEIUM_STATE_SUCCESS", "message": "Generated 3 completions" },
//	  "completionItems": [{
//	    "completion": {
//	      "completionId": "7decfce5-...",
//	      "text": "\tfmt.Println(\"Hello, World!\")",
//	      "stop": "\n",
//	      "score": -0.97,
//	      "tokens": ["1","9906","11","4435","23849"],
//	      "decodedTokens": ["\"","Hello",","," World","!\")\n"],
//	      "probabilities": [0.85,0.60,0.46,0.70,0.66],
//	      "adjustedProbabilities": [0,0,0,0,0],
//	      "generatedLength": "5",
//	      "stopReason": "STOP_REASON_STOP_PATTERN",
//	      "originalText": "\"Hello, World!\")\n"
//	    },
//	    "range": {
//	      "startOffset": "28",
//	      "endOffset": "41",
//	      "startPosition": { "row": "3" },
//	      "endPosition": { "row": "3", "col": "13" }
//	    },
//	    "source": "COMPLETION_SOURCE_TYPING_AS_SUGGESTED",
//	    "completionParts": [{
//	      "text": "\"Hello, World!\")",
//	      "offset": "41",
//	      "type": "COMPLETION_PART_TYPE_INLINE",
//	      "prefix": "\tfmt.Println(",
//	      "line": "3"
//	    }]
//	  }],
//	  "filteredCompletionItems": [...],
//	  "promptId": "8418cdd3-...",
//	  "codeRanges": [{ "source": "CODE_SOURCE_BASE", "endOffset": "42" }]
//	}
package windsurf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cursortab/buffer"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/types"
)

type windsurfInt int

func (i *windsurfInt) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*i = windsurfInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*i = windsurfInt(n)
	return nil
}

// InfoProvider is the minimum interface the Windsurf provider needs from the
// buffer. *buffer.NvimBuffer satisfies this via duck typing. The eval harness
// provides a cassette-backed implementation that returns static info.
type InfoProvider interface {
	GetWindsurfInfo() (*buffer.WindsurfInfo, error)
}

const windsurfBlockPartType = "COMPLETION_PART_TYPE_BLOCK"
const windsurfInlineMaskPartType = "COMPLETION_PART_TYPE_INLINE_MASK"

var languageEnum = map[string]int{
	"unspecified":  0,
	"c":            1,
	"clojure":      2,
	"coffeescript": 3,
	"cpp":          4,
	"csharp":       5,
	"css":          6,
	"cudacpp":      7,
	"dockerfile":   8,
	"go":           9,
	"groovy":       10,
	"handlebars":   11,
	"haskell":      12,
	"hcl":          13,
	"html":         14,
	"ini":          15,
	"java":         16,
	"javascript":   17,
	"json":         18,
	"julia":        19,
	"kotlin":       20,
	"latex":        21,
	"less":         22,
	"lua":          23,
	"makefile":     24,
	"markdown":     25,
	"objectivec":   26,
	"objectivecpp": 27,
	"perl":         28,
	"php":          29,
	"plaintext":    30,
	"protobuf":     31,
	"pbtxt":        32,
	"python":       33,
	"r":            34,
	"ruby":         35,
	"rust":         36,
	"sass":         37,
	"scala":        38,
	"scss":         39,
	"shell":        40,
	"sql":          41,
	"starlark":     42,
	"swift":        43,
	"tsx":          44,
	"typescript":   45,
	"visualbasic":  46,
	"vue":          47,
	"xml":          48,
	"xsl":          49,
	"yaml":         50,
	"svelte":       51,
}

var filetypeAliases = map[string]string{
	"bash":   "shell",
	"coffee": "coffeescript",
	"cs":     "csharp",
	"cuda":   "cudacpp",
	"dosini": "ini",
	"make":   "makefile",
	"objc":   "objectivec",
	"objcpp": "objectivecpp",
	"proto":  "protobuf",
	"raku":   "perl",
	"sh":     "shell",
	"text":   "plaintext",
}

var extensionLanguages = map[string]string{
	"go": "go", "py": "python", "js": "javascript", "ts": "typescript",
	"tsx": "tsx", "jsx": "javascript", "java": "java", "rs": "rust",
	"c": "c", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "h": "c",
	"hpp": "cpp", "lua": "lua", "rb": "ruby", "php": "php",
	"sh": "shell", "bash": "shell", "sql": "sql", "md": "markdown",
	"html": "html", "css": "css", "scss": "scss", "less": "less",
	"vue": "vue", "svelte": "svelte", "kt": "kotlin", "swift": "swift",
	"scala": "scala", "r": "r", "json": "json", "yaml": "yaml",
	"yml": "yaml", "xml": "xml", "toml": "ini", "dockerfile": "dockerfile",
	"makefile": "makefile", "ex": "elixir", "exs": "elixir",
	"erl": "erlang", "clj": "clojure", "hs": "haskell",
	"dart": "dart", "proto": "protobuf", "tex": "latex",
}

type windsurfPos struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type windsurfResponseRange struct {
	StartOffset   string `json:"startOffset"`
	EndOffset     string `json:"endOffset"`
	StartPosition struct {
		Row string `json:"row"`
	} `json:"startPosition"`
	EndPosition struct {
		Row string `json:"row"`
		Col string `json:"col"`
	} `json:"endPosition"`
}

type windsurfCompletionPart struct {
	Text   string `json:"text"`
	Offset string `json:"offset"`
	Type   string `json:"type"`
	Prefix string `json:"prefix"`
	Line   string `json:"line"`
}

type windsurfCompletion struct {
	CompletionID          string    `json:"completionId"`
	Text                  string    `json:"text"`
	Stop                  string    `json:"stop"`
	Score                 float64   `json:"score"`
	Tokens                []string  `json:"tokens"`
	DecodedTokens         []string  `json:"decodedTokens"`
	Probabilities         []float64 `json:"probabilities"`
	AdjustedProbabilities []float64 `json:"adjustedProbabilities"`
	GeneratedLength       string    `json:"generatedLength"`
	StopReason            string    `json:"stopReason"`
	OriginalText          string    `json:"originalText"`
}

type windsurfCompletionItem struct {
	Completion      windsurfCompletion       `json:"completion"`
	Range           windsurfResponseRange    `json:"range"`
	Source          string                   `json:"source"`
	CompletionParts []windsurfCompletionPart `json:"completionParts"`
}

type windsurfState struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type windsurfResponse struct {
	State                   windsurfState            `json:"state"`
	CompletionItems         []windsurfCompletionItem `json:"completionItems"`
	FilteredCompletionItems []windsurfCompletionItem `json:"filteredCompletionItems"`
	PromptID                string                   `json:"promptId"`
}

type windsurfMetadata struct {
	APIKey           string `json:"api_key"`
	IDEName          string `json:"ide_name"`
	IDEVersion       string `json:"ide_version"`
	ExtensionName    string `json:"extension_name"`
	ExtensionVersion string `json:"extension_version"`
	RequestID        int    `json:"request_id"`
}

type windsurfEditorOptions struct {
	TabSize      int  `json:"tab_size"`
	InsertSpaces bool `json:"insert_spaces"`
}

type windsurfDocument struct {
	Text           string      `json:"text"`
	EditorLanguage string      `json:"editor_language"`
	Language       int         `json:"language"`
	CursorOffset   int         `json:"cursor_offset,omitempty"`
	CursorPosition windsurfPos `json:"cursor_position"`
	AbsoluteURI    string      `json:"absolute_uri"`
	WorkspaceURI   string      `json:"workspace_uri"`
	LineEnding     string      `json:"line_ending"`
}

type windsurfRequest struct {
	Metadata      windsurfMetadata      `json:"metadata"`
	EditorOptions windsurfEditorOptions `json:"editor_options"`
	Document      windsurfDocument      `json:"document"`
}

type windsurfAcceptRequest struct {
	Metadata     windsurfMetadata `json:"metadata"`
	CompletionID string           `json:"completion_id"`
}

type windsurfSupercompleteRequest struct {
	Metadata                      windsurfMetadata      `json:"metadata"`
	Document                      windsurfDocument      `json:"document"`
	EditorOptions                 windsurfEditorOptions `json:"editor_options"`
	SupercompleteTriggerCondition string                `json:"supercomplete_trigger_condition"`
	DisableTabJump                bool                  `json:"disable_tab_jump"`
}

type windsurfSupercompleteResponse struct {
	CompletionID string `json:"completionId"`
	PromptID     string `json:"promptId"`
	RequestUID   string `json:"requestUid"`
	RawJSON      string `json:"-"`
	FilterReason *struct {
		Reason string `json:"reason"`
	} `json:"filterReason"`
	Diff       *windsurfSupercompleteDiff    `json:"diff"`
	TabJump    *windsurfSupercompleteTabJump `json:"tabJump"`
	Suggestion struct {
		Case  string `json:"case"`
		Value struct {
			SelectionStartLine windsurfInt           `json:"selectionStartLine"`
			SelectionEndLine   windsurfInt           `json:"selectionEndLine"`
			CharacterDiff      windsurfCharacterDiff `json:"characterDiff"`
			CursorPosition     *windsurfResponsePos  `json:"cursorPosition"`
			Path               string                `json:"path"`
			JumpPosition       *windsurfResponsePos  `json:"jumpPosition"`
		} `json:"value"`
		Diff    *windsurfSupercompleteDiff    `json:"diff"`
		TabJump *windsurfSupercompleteTabJump `json:"tabJump"`
	} `json:"suggestion"`
}

type windsurfSupercompleteDiff struct {
	SelectionStartLine windsurfInt           `json:"selectionStartLine"`
	SelectionEndLine   windsurfInt           `json:"selectionEndLine"`
	CharacterDiff      windsurfCharacterDiff `json:"characterDiff"`
	CursorPosition     *windsurfResponsePos  `json:"cursorPosition"`
	Path               string                `json:"path"`
}

type windsurfSupercompleteTabJump struct {
	Path         string               `json:"path"`
	JumpPosition *windsurfResponsePos `json:"jumpPosition"`
	IsImport     bool                 `json:"isImport"`
}

type windsurfResponsePos struct {
	Row windsurfInt `json:"row"`
	Col windsurfInt `json:"col"`
}

type windsurfCharacterDiff struct {
	Changes []windsurfDiffChange `json:"changes"`
}

type windsurfDiffChange struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Provider struct {
	buffer     InfoProvider
	httpClient *http.Client
	reqCounter int64
}

func NewProvider(buf InfoProvider) *Provider {
	return &Provider{
		buffer: buf,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (p *Provider) nextRequestID() int {
	return int(atomic.AddInt64(&p.reqCounter, 1))
}

func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	p.httpClient.Transport = rt
}

func (p *Provider) GetContextLimits() engine.ContextLimits {
	return engine.ContextLimits{
		MaxUserActions:     -1,
		FileChunkLines:     -1,
		MaxRecentSnapshots: -1,
		MaxDiffBytes:       -1,
		MaxChangedSymbols:  -1,
		MaxSiblings:        -1,
		MaxInputLines:      -1,
		MaxInputBytes:      -1,
	}
}

func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("windsurf.GetCompletion")()

	info, err := p.buffer.GetWindsurfInfo()
	if err != nil {
		logger.Error("failed to get windsurf info: %v", err)
		return p.emptyResponse(), nil
	}
	if info == nil || !info.Healthy {
		logger.Debug("windsurf: server not healthy")
		return p.emptyResponse(), nil
	}

	if info.CSRFToken != "" {
		resp := p.getSupercomplete(ctx, req, info)
		if len(resp.Completions) > 0 || resp.CursorTarget != nil {
			return resp, nil
		}
	} else {
		candidates := discoverWindsurfIDEInfos(info.APIKey)
		if len(candidates) > 0 {
			logger.Debug("windsurf: trying %d Windsurf IDE supercomplete candidates", len(candidates))
		}
		for _, candidate := range candidates {
			resp := p.getSupercomplete(ctx, req, candidate)
			if len(resp.Completions) > 0 || resp.CursorTarget != nil {
				return resp, nil
			}
		}
	}

	lineEnding := "\n"
	language := resolveLanguage(req.FilePath)

	absFilePath, _ := filepath.Abs(req.FilePath)
	absWorkspacePath, _ := filepath.Abs(req.WorkspacePath)

	text := strings.Join(req.Lines, lineEnding)
	if len(req.Lines) > 0 {
		text += lineEnding
	}
	cursorOffset := cursorByteOffset(req.Lines, req.CursorRow, req.CursorCol)

	reqID := p.nextRequestID()
	wsReq := windsurfRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "neovim",
			IDEVersion:       "0.10.0",
			ExtensionName:    "neovim",
			ExtensionVersion: "1.20.9",
			RequestID:        reqID,
		},
		EditorOptions: windsurfEditorOptions{
			TabSize:      4,
			InsertSpaces: true,
		},
		Document: windsurfDocument{
			Text:           text,
			EditorLanguage: language,
			Language:       languageEnum[language],
			CursorOffset:   cursorOffset,
			CursorPosition: windsurfPos{
				Row: req.CursorRow - 1,
				Col: req.CursorCol,
			},
			AbsoluteURI:  "file://" + absFilePath,
			WorkspaceURI: "file://" + absWorkspacePath,
			LineEnding:   lineEnding,
		},
	}

	body, err := json.Marshal(wsReq)
	if err != nil {
		logger.Error("windsurf: failed to marshal request: %v", err)
		return p.emptyResponse(), nil
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/GetCompletions", info.Port)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Error("windsurf: failed to create request: %v", err)
		return p.emptyResponse(), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		logger.Debug("windsurf: request failed: %v", err)
		return p.emptyResponse(), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Debug("windsurf: non-200 response %d: %s", resp.StatusCode, string(respBody))
		return p.emptyResponse(), nil
	}

	var wsResp windsurfResponse
	if err := json.NewDecoder(resp.Body).Decode(&wsResp); err != nil {
		logger.Error("windsurf: failed to decode response: %v", err)
		return p.emptyResponse(), nil
	}

	if wsResp.State.State != "CODEIUM_STATE_SUCCESS" {
		logger.Debug("windsurf: non-success state: %v", wsResp.State)
		return p.emptyResponse(), nil
	}

	return p.convertResponse(&wsResp, req)
}

func (p *Provider) getSupercomplete(ctx context.Context, req *types.CompletionRequest, info *buffer.WindsurfInfo) *types.CompletionResponse {
	lineEnding := "\n"
	language := resolveLanguage(req.FilePath)

	absFilePath, _ := filepath.Abs(req.FilePath)
	absWorkspacePath, _ := filepath.Abs(req.WorkspacePath)
	text := buildDocumentText(req.Lines)

	p.reqCounter++
	wsReq := windsurfSupercompleteRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "windsurf",
			IDEVersion:       "2.3.9",
			ExtensionName:    "windsurf",
			ExtensionVersion: "1.48.2",
			RequestID:        p.reqCounter,
		},
		Document: windsurfDocument{
			Text:           text,
			EditorLanguage: language,
			Language:       languageEnum[language],
			CursorOffset:   cursorByteOffset(req.Lines, req.CursorRow, req.CursorCol),
			CursorPosition: windsurfPos{
				Row: req.CursorRow - 1,
				Col: req.CursorCol,
			},
			AbsoluteURI:  "file://" + absFilePath,
			WorkspaceURI: "file://" + absWorkspacePath,
			LineEnding:   lineEnding,
		},
		EditorOptions: windsurfEditorOptions{
			TabSize:      4,
			InsertSpaces: true,
		},
		SupercompleteTriggerCondition: "SUPERCOMPLETE_TRIGGER_CONDITION_TYPING",
		DisableTabJump:                false,
	}

	body, err := json.Marshal(wsReq)
	if err != nil {
		logger.Debug("windsurf: failed to marshal supercomplete request: %v", err)
		return p.emptyResponse()
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/HandleStreamingTabV2", info.Port)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Debug("windsurf: failed to create supercomplete request: %v", err)
		return p.emptyResponse()
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-codeium-csrf-token", info.CSRFToken)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		logger.Debug("windsurf: supercomplete request failed: %v", err)
		return p.emptyResponse()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Debug("windsurf: supercomplete non-200 response %d: %s", resp.StatusCode, string(respBody))
		return p.emptyResponse()
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Debug("windsurf: failed to read supercomplete response: %v", err)
		return p.emptyResponse()
	}

	var wsResp windsurfSupercompleteResponse
	if err := json.Unmarshal(respBody, &wsResp); err != nil {
		logger.Debug("windsurf: failed to decode supercomplete response: %v", err)
		return p.emptyResponse()
	}
	wsResp.RawJSON = string(respBody)

	return p.convertSupercompleteResponse(&wsResp, req)
}

func (p *Provider) SendMetric(ctx context.Context, event metrics.Event) {
	if event.Type != metrics.EventAccepted {
		return
	}
	if event.Info.ID == "" {
		return
	}

	info, err := p.buffer.GetWindsurfInfo()
	if err != nil || info == nil || !info.Healthy {
		return
	}

	reqID := p.nextRequestID()
	acceptReq := windsurfAcceptRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "neovim",
			IDEVersion:       "0.10.0",
			ExtensionName:    "neovim",
			ExtensionVersion: "1.20.9",
			RequestID:        reqID,
		},
		CompletionID: event.Info.ID,
	}

	body, err := json.Marshal(acceptReq)
	if err != nil {
		logger.Debug("windsurf: failed to marshal accept request: %v", err)
		return
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/AcceptCompletion", info.Port)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Debug("windsurf: failed to create accept request: %v", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		logger.Debug("windsurf: accept request failed: %v", err)
		return
	}
	resp.Body.Close()
}

func (p *Provider) convertResponse(wsResp *windsurfResponse, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	if len(wsResp.CompletionItems) == 0 {
		return p.emptyResponse(), nil
	}

	var completions []*types.Completion
	var metricsInfo *types.MetricsInfo

	for i, item := range wsResp.CompletionItems {
		completion := p.convertSingleItem(item, req, i)
		if completion != nil {
			completions = append(completions, completion)
			if metricsInfo == nil && item.Completion.CompletionID != "" {
				metricsInfo = &types.MetricsInfo{
					ID: item.Completion.CompletionID,
				}
			}
		}
	}

	if len(completions) == 0 {
		return p.emptyResponse(), nil
	}

	return &types.CompletionResponse{
		Completions: completions,
		MetricsInfo: metricsInfo,
	}, nil
}

func (p *Provider) convertSupercompleteResponse(wsResp *windsurfSupercompleteResponse, req *types.CompletionRequest) *types.CompletionResponse {
	if wsResp.FilterReason != nil {
		logger.Debug("windsurf: supercomplete filtered: %s", wsResp.FilterReason.Reason)
		return p.emptyResponse()
	}

	target := wsResp.cursorTarget(req)
	if target != nil && wsResp.hasTabJump() {
		return &types.CompletionResponse{
			Completions:  []*types.Completion{},
			CursorTarget: target,
			MetricsInfo:  supercompleteMetricsInfo(wsResp),
		}
	}

	if !wsResp.hasDiff() {
		logger.Debug("windsurf: supercomplete unsupported suggestion case: %s body=%s", wsResp.Suggestion.Case, logPreview(wsResp.RawJSON))
		return p.emptyResponse()
	}

	finalLines := splitDocumentLines(buildSupercompleteFinalText(req.Lines, wsResp))
	if slices.Equal(finalLines, req.Lines) {
		return p.emptyResponse()
	}

	startLine, endLine, newLines := changedLineRange(req.Lines, finalLines)
	if len(newLines) == 0 {
		newLines = []string{""}
	}

	return &types.CompletionResponse{
		Completions: []*types.Completion{{
			StartLine:  startLine,
			EndLineInc: endLine,
			Lines:      newLines,
		}},
		CursorTarget: target,
		MetricsInfo:  supercompleteMetricsInfo(wsResp),
	}
}

func supercompleteMetricsInfo(wsResp *windsurfSupercompleteResponse) *types.MetricsInfo {
	if wsResp.CompletionID == "" {
		return nil
	}
	return &types.MetricsInfo{ID: wsResp.CompletionID}
}

func (p *Provider) convertSingleItem(item windsurfCompletionItem, req *types.CompletionRequest, idx int) *types.Completion {
	documentText := buildDocumentText(req.Lines)
	startOffset, endOffset, ok := p.resolveItemOffsets(item, idx, len(documentText))
	if !ok {
		return nil
	}

	startLine, endLine, ok := p.resolveReplacementLines(item, req, startOffset, endOffset, documentText, idx)
	if !ok {
		return nil
	}

	completionText := p.buildCompletionText(item, documentText, startOffset, endOffset)
	if completionText == "" {
		logger.Debug("windsurf: item %d has empty completion text", idx)
		return nil
	}

	newLines := strings.Split(completionText, "\n")

	// Always append the suffix from the original line after the completion
	// range. When completion parts are present we skip any INLINE_MASK parts
	// above, so the suffix must be preserved here to avoid truncation of the
	// line tail.
	endCol, _ := strconv.Atoi(item.Range.EndPosition.Col)
	if endCol > 0 && endLine >= 1 && endLine <= len(req.Lines) {
		lastOrigLine := req.Lines[endLine-1]
		if endCol < len(lastOrigLine) && len(newLines) > 0 {
			newLines[len(newLines)-1] += lastOrigLine[endCol:]
		}
	}

	origLines := req.Lines[startLine-1 : endLine]
	if slices.Equal(newLines, origLines) {
		logger.Debug("windsurf: item %d is no-op", idx)
		return nil
	}

	logger.Debug("windsurf: converted item %d startLine=%d endLine=%d newLines=%d", idx, startLine, endLine, len(newLines))

	return &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLine,
		Lines:      newLines,
	}
}

func buildDocumentText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n") + "\n"
}

func splitDocumentLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func buildSupercompleteFinalText(lines []string, resp *windsurfSupercompleteResponse) string {
	diff := resp.supercompleteDiff()
	var replacement strings.Builder
	for _, change := range diff.CharacterDiff.Changes {
		if isWindsurfInsertedOrUnchanged(change.Type) {
			replacement.WriteString(change.Text)
		}
	}

	origLines := splitDocumentLines(buildDocumentText(lines))
	startLine := min(int(diff.SelectionStartLine), len(origLines))
	endLine := min(int(diff.SelectionEndLine), len(origLines))

	finalLines := append([]string{}, origLines[:startLine]...)
	if replacement.Len() > 0 {
		finalLines = append(finalLines, splitDocumentLines(replacement.String())...)
	}
	if endLine < len(origLines) {
		finalLines = append(finalLines, origLines[endLine:]...)
	}
	return buildDocumentText(finalLines)
}

func (r *windsurfSupercompleteResponse) supercompleteDiff() windsurfSupercompleteDiff {
	if r.Diff != nil {
		return *r.Diff
	}
	if r.Suggestion.Diff != nil {
		return *r.Suggestion.Diff
	}
	return windsurfSupercompleteDiff{
		SelectionStartLine: r.Suggestion.Value.SelectionStartLine,
		SelectionEndLine:   r.Suggestion.Value.SelectionEndLine,
		CharacterDiff:      r.Suggestion.Value.CharacterDiff,
		CursorPosition:     r.Suggestion.Value.CursorPosition,
		Path:               r.Suggestion.Value.Path,
	}
}

func (r *windsurfSupercompleteResponse) cursorTarget(req *types.CompletionRequest) *types.CursorPredictionTarget {
	var path string
	var pos *windsurfResponsePos
	if r.TabJump != nil {
		path = r.TabJump.Path
		pos = r.TabJump.JumpPosition
	} else if r.Suggestion.Case == "tabJump" {
		path = r.Suggestion.Value.Path
		pos = r.Suggestion.Value.JumpPosition
	} else if r.Suggestion.TabJump != nil {
		path = r.Suggestion.TabJump.Path
		pos = r.Suggestion.TabJump.JumpPosition
	} else {
		diff := r.supercompleteDiff()
		path = diff.Path
		pos = diff.CursorPosition
	}
	if pos == nil || pos.Row < 0 {
		return nil
	}

	relativePath := path
	if relativePath == "" {
		relativePath = req.FilePath
	}
	if req.WorkspacePath != "" {
		if rel, err := filepath.Rel(req.WorkspacePath, relativePath); err == nil && !strings.HasPrefix(rel, "..") {
			relativePath = rel
		}
	}

	lineNumber := int(pos.Row) + 1
	expectedContent := ""
	if lineNumber >= 1 && lineNumber <= len(req.Lines) {
		expectedContent = req.Lines[lineNumber-1]
	}
	return &types.CursorPredictionTarget{
		RelativePath:    relativePath,
		LineNumber:      int32(lineNumber),
		ExpectedContent: expectedContent,
		ShouldRetrigger: true,
	}
}

func (r *windsurfSupercompleteResponse) hasDiff() bool {
	return r.Diff != nil || r.Suggestion.Diff != nil || r.Suggestion.Case == "diff"
}

func (r *windsurfSupercompleteResponse) hasTabJump() bool {
	return r.TabJump != nil || r.Suggestion.TabJump != nil || r.Suggestion.Case == "tabJump"
}

func logPreview(s string) string {
	const max = 1200
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func changedLineRange(oldLines, newLines []string) (int, int, []string) {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	oldSuffix := len(oldLines)
	newSuffix := len(newLines)
	for oldSuffix > prefix && newSuffix > prefix && oldLines[oldSuffix-1] == newLines[newSuffix-1] {
		oldSuffix--
		newSuffix--
	}

	startLine := prefix + 1
	endLine := oldSuffix
	if endLine < startLine {
		endLine = startLine
	}
	return startLine, endLine, newLines[prefix:newSuffix]
}

func isWindsurfInsertedOrUnchanged(changeType string) bool {
	switch changeType {
	case "INSERT", "DIFF_CHANGE_TYPE_INSERT", "insert", "2":
		return true
	case "UNCHANGED", "DIFF_CHANGE_TYPE_UNCHANGED", "unchanged", "1":
		return true
	default:
		return false
	}
}

func cursorByteOffset(lines []string, row, col int) int {
	if row < 1 {
		row = 1
	}
	if row > len(lines) {
		row = len(lines)
	}
	offset := 0
	for i := 0; i < row-1; i++ {
		offset += len(lines[i]) + 1
	}
	if row >= 1 && row <= len(lines) {
		if col < 0 {
			col = 0
		}
		if col > len(lines[row-1]) {
			col = len(lines[row-1])
		}
		offset += col
	}
	return offset
}

func (p *Provider) resolveItemOffsets(item windsurfCompletionItem, idx, documentLen int) (int, int, bool) {
	startOffset, startErr := strconv.Atoi(item.Range.StartOffset)
	endOffset, endErr := strconv.Atoi(item.Range.EndOffset)
	if startErr != nil || endErr != nil {
		logger.Debug("windsurf: item %d missing valid offsets start=%q end=%q", idx, item.Range.StartOffset, item.Range.EndOffset)
		return 0, 0, false
	}

	if startOffset < 0 || endOffset < startOffset || endOffset > documentLen {
		logger.Debug("windsurf: item %d offsets out of bounds start=%d end=%d len=%d", idx, startOffset, endOffset, documentLen)
		return 0, 0, false
	}

	return startOffset, endOffset, true
}

func (p *Provider) resolveReplacementLines(item windsurfCompletionItem, req *types.CompletionRequest, startOffset, endOffset int, documentText string, idx int) (int, int, bool) {
	startLine, _ := byteOffsetToLineCol(documentText, startOffset)
	endLine, endCol := byteOffsetToLineCol(documentText, endOffset)

	if endOffset > startOffset && endCol == 0 && endLine > startLine {
		endLine--
	}

	if startLine < 1 || startLine > len(req.Lines)+1 {
		logger.Debug("windsurf: item %d start line %d out of bounds", idx, startLine)
		return 0, 0, false
	}

	if len(req.Lines) == 0 {
		return 1, 1, true
	}

	if startLine > len(req.Lines) {
		startLine = len(req.Lines)
	}
	if endLine > len(req.Lines) {
		endLine = len(req.Lines)
	}
	if endLine < startLine {
		endLine = startLine
	}

	_ = item
	return startLine, endLine, true
}

func (p *Provider) buildCompletionText(item windsurfCompletionItem, documentText string, startOffset, endOffset int) string {
	if len(item.CompletionParts) == 0 {
		return item.Completion.Text
	}

	var b strings.Builder
	cur := startOffset

	for _, part := range item.CompletionParts {
		// Skip inline-mask parts: these are display-only masks that show the
		// complete visible line (insert + suffix). They should not be treated as
		// additional inserted text, otherwise the preview duplicates content.
		if part.Type == windsurfInlineMaskPartType {
			curOff, _ := strconv.Atoi(part.Offset)
			if curOff > cur {
				// advance cur to avoid re-including replaced content
				cur = curOff
			}
			continue
		}
		offset, err := strconv.Atoi(part.Offset)
		if err != nil {
			continue
		}
		if offset < cur {
			offset = cur
		}
		if offset > endOffset {
			offset = endOffset
		}

		b.WriteString(documentText[cur:offset])
		if part.Type == windsurfBlockPartType {
			b.WriteByte('\n')
		}
		b.WriteString(part.Text)
		cur = offset
	}

	b.WriteString(documentText[cur:endOffset])
	return b.String()
}

func byteOffsetToLineCol(text string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}

	line := 1
	lineStart := 0
	for i := 0; i < offset; i++ {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}

	return line, offset - lineStart
}

// LanguageEnum returns the numeric language ID expected by the Windsurf API.
func LanguageEnum(language string) int {
	return languageEnum[language]
}

func resolveLanguage(filePath string) string {
	parts := strings.Split(filePath, ".")
	if len(parts) < 2 {
		return "plaintext"
	}
	ext := parts[len(parts)-1]

	lang := extensionLanguages[ext]
	if lang == "" {
		base := strings.ToLower(parts[len(parts)-2] + "." + ext)
		if base == "dockerfile" || base == "makefile" {
			lang = base
		}
	}
	if lang == "" {
		lang = "plaintext"
	}

	if alias, ok := filetypeAliases[lang]; ok {
		lang = alias
	}

	return lang
}

func (p *Provider) emptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
}

func discoverWindsurfIDEInfos(apiKey string) []*buffer.WindsurfInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	var infos []*buffer.WindsurfInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil || !strings.Contains(exe, "/windsurf/") || !strings.HasSuffix(exe, "language_server_linux_x64") {
			continue
		}

		env, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		csrfToken := environValue(env, "WINDSURF_CSRF_TOKEN")
		if csrfToken == "" {
			continue
		}

		for _, port := range processListenPorts(pid) {
			infos = append(infos, &buffer.WindsurfInfo{
				Healthy:   true,
				Port:      port,
				APIKey:    apiKey,
				CSRFToken: csrfToken,
			})
		}
	}
	return infos
}

func environValue(env []byte, key string) string {
	prefix := key + "="
	for _, part := range strings.Split(string(env), "\x00") {
		if strings.HasPrefix(part, prefix) {
			return strings.TrimPrefix(part, prefix)
		}
	}
	return ""
}

func processListenPorts(pid int) []int {
	inodes := map[string]bool{}
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}

	var ports []int
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		for _, socket := range listenSockets(path) {
			if inodes[socket.inode] {
				ports = append(ports, socket.port)
			}
		}
	}
	return ports
}

type listenSocket struct {
	port  int
	inode string
}

func listenSockets(path string) []listenSocket {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	sockets := make([]listenSocket, 0, len(lines))
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		hostPort := strings.Split(fields[1], ":")
		if len(hostPort) != 2 {
			continue
		}
		port64, err := strconv.ParseInt(hostPort[1], 16, 32)
		if err != nil {
			continue
		}
		sockets = append(sockets, listenSocket{port: int(port64), inode: fields[9]})
	}
	return sockets
}
