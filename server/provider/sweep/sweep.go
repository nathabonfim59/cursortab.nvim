// Package sweep implements the Sweep Next-Edit model provider.
//
// Prompt format (sent as a single text prompt to /v1/completions):
//
//	<|file_sep|>file.go.diff          (diff history, if any)
//	<<<<<<< ORIGINAL
//	old line
//	=======
//	new line
//	>>>>>>> UPDATED
//
//	<|file_sep|>context/treesitter    (omitted if no treesitter context)
//	Enclosing scope: func handleRequest(...) {
//	Sibling: func otherFunc() {
//	Import: import "net/http"
//
//	<|file_sep|>context/staged_diff   (omitted if not COMMIT_EDITMSG)
//	(full unified diff if ≤4KB, or extracted symbols in git diff format:)
//	+func newHelper(ctx context.Context) error {
//	-func oldHelper() error {
//
//	<|file_sep|>original/file.go      (file content before edits)
//	...original lines in trimmed window...
//
//	<|file_sep|>current/file.go       (file content as-is now)
//	...current lines in trimmed window...
//
//	<|file_sep|>updated/file.go       (model completes from here)
//
// Stop tokens: <|file_sep|>, </s>
package sweep

import (
	"fmt"
	"strings"

	"cursortab/client/openai"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new Sweep Next-Edit model provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:          "sweep",
		Config:        config,
		Client:        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType: provider.StreamingLines,
		Preprocessors: []provider.Preprocessor{
			provider.TrimContent(),
		},
		DiffBuilder:   provider.FormatDiffHistoryOriginalUpdated("<|file_sep|>%s.diff\n"),
		PromptBuilder: buildPrompt,
		Postprocessors: []provider.Postprocessor{
			provider.RejectEmpty(),
			provider.ValidateAnchorPosition(0.25),
			provider.AnchorTruncation(0.75),
			parseCompletion,
		},
		Validators: []provider.Validator{
			provider.ValidateFirstLineAnchor(0.25),
		},
		StopTokens: []string{"<|file_sep|>", "</s>"},
	}
}

func buildPrompt(p *provider.Provider, ctx *provider.Context) *openai.CompletionRequest {
	req := ctx.Request
	var promptBuilder strings.Builder

	if len(req.Lines) == 0 {
		promptBuilder.WriteString("<|file_sep|>original/")
		promptBuilder.WriteString(req.FilePath)
		promptBuilder.WriteString("\n\n")
		promptBuilder.WriteString("<|file_sep|>current/")
		promptBuilder.WriteString(req.FilePath)
		promptBuilder.WriteString("\n\n")
		promptBuilder.WriteString("<|file_sep|>updated/")
		promptBuilder.WriteString(req.FilePath)
		promptBuilder.WriteString("\n")

		return &openai.CompletionRequest{
			Model:       p.Config.ProviderModel,
			Prompt:      promptBuilder.String(),
			Temperature: p.Config.ProviderTemperature,
			MaxTokens:   p.Config.ProviderMaxTokens,
			TopK:        p.Config.ProviderTopK,
			Stop:        []string{"<|file_sep|>", "</s>"},
			N:           1,
			Echo:        false,
		}
	}

	diffSection := ""
	if p.DiffBuilder != nil {
		diffSection = p.DiffBuilder(req.FileDiffHistories)
	}
	originalLines := getTrimmedOriginalContent(req, ctx.WindowStart, len(ctx.TrimmedLines))

	if diffSection != "" {
		promptBuilder.WriteString(diffSection)
	}

	if ts := formatTreesitterSection(req); ts != "" {
		promptBuilder.WriteString(ts)
	}

	if gd := formatGitDiffSection(req); gd != "" {
		promptBuilder.WriteString(gd)
	}

	promptBuilder.WriteString("<|file_sep|>original/")
	promptBuilder.WriteString(req.FilePath)
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(strings.Join(originalLines, "\n"))
	promptBuilder.WriteString("\n")

	promptBuilder.WriteString("<|file_sep|>current/")
	promptBuilder.WriteString(req.FilePath)
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(strings.Join(ctx.TrimmedLines, "\n"))
	promptBuilder.WriteString("\n")

	promptBuilder.WriteString("<|file_sep|>updated/")
	promptBuilder.WriteString(req.FilePath)
	promptBuilder.WriteString("\n")

	return &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      promptBuilder.String(),
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        []string{"<|file_sep|>", "</s>"},
		N:           1,
		Echo:        false,
	}
}

func formatTreesitterSection(req *types.CompletionRequest) string {
	ts := req.GetTreesitter()
	if ts == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("<|file_sep|>context/treesitter\n")

	if ts.EnclosingSignature != "" {
		fmt.Fprintf(&b, "Enclosing scope: %s\n", ts.EnclosingSignature)
	}

	for _, s := range ts.Siblings {
		fmt.Fprintf(&b, "Sibling: %s\n", s.Signature)
	}

	for _, imp := range ts.Imports {
		fmt.Fprintf(&b, "Import: %s\n", imp)
	}

	return b.String()
}

func formatGitDiffSection(req *types.CompletionRequest) string {
	gd := req.GetGitDiff()
	if gd == nil || gd.Diff == "" {
		return ""
	}
	return "<|file_sep|>context/staged_diff\n" + gd.Diff
}

func getTrimmedOriginalContent(req *types.CompletionRequest, trimOffset, lineCount int) []string {
	sourceLines := req.PreviousLines
	if len(sourceLines) == 0 {
		sourceLines = req.Lines
	}

	windowStart := trimOffset
	windowEnd := trimOffset + lineCount

	if windowStart >= len(sourceLines) {
		return []string{}
	}
	if windowEnd > len(sourceLines) {
		windowEnd = len(sourceLines)
	}

	return sourceLines[windowStart:windowEnd]
}

func parseCompletion(p *provider.Provider, ctx *provider.Context) (*types.CompletionResponse, bool) {
	completionText := ctx.Result.Text
	req := ctx.Request

	completionText = strings.TrimSuffix(completionText, "<|file_sep|>")
	completionText = strings.TrimSuffix(completionText, "</s>")
	completionText = strings.TrimRight(completionText, " \t\n\r")

	windowStart := ctx.WindowStart
	windowEnd := ctx.WindowEnd
	if windowStart < 0 {
		windowStart = 0
	}
	if windowEnd > len(req.Lines) {
		windowEnd = len(req.Lines)
	}
	if windowStart >= windowEnd || windowStart >= len(req.Lines) {
		return p.EmptyResponse(), true
	}

	oldLines := req.Lines[windowStart:windowEnd]
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")

	if completionText == oldText {
		return p.EmptyResponse(), true
	}

	newLines := strings.Split(completionText, "\n")

	endLineInc := ctx.EndLineInc
	if endLineInc == 0 {
		endLineInc = min(windowStart+len(newLines), windowEnd)
	}

	return p.BuildCompletion(ctx, windowStart+1, endLineInc, newLines)
}
