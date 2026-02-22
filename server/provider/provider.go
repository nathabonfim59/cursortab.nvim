package provider

import (
	"context"
	"cursortab/client/openai"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/types"
	"errors"
	"fmt"
)

// StreamingType defines how completion content is streamed
type StreamingType int

const (
	// StreamingNone indicates batch mode (no streaming)
	StreamingNone StreamingType = iota
	// StreamingLines indicates line-by-line streaming (sweep, zeta, fim)
	StreamingLines
	// StreamingTokens indicates token-by-token streaming (inline)
	StreamingTokens
)

// Compile-time checks that Provider implements the required interfaces
var _ engine.Provider = (*Provider)(nil)
var _ engine.LineStreamProvider = (*Provider)(nil)
var _ engine.TokenStreamProvider = (*Provider)(nil)

// Client interface for API calls (enables mocking in tests)
type Client interface {
	DoCompletion(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResponse, error)
	DoLineStream(ctx context.Context, req *openai.CompletionRequest, maxLines int, stopTokens []string) *openai.LineStream
	DoTokenStream(ctx context.Context, req *openai.CompletionRequest, maxChars int, stopTokens []string) *openai.LineStream
}

// Validator validates streaming content (e.g., first line anchor validation)
// Called after receiving the first line. Return error to cancel the stream.
type Validator func(p *Provider, ctx *Context, firstLine string) error

// DiffHistoryBuilder processes multi-file diff history into a string for the prompt
type DiffHistoryBuilder func(history []*types.FileDiffHistory) string

// Context carries data through the completion pipeline
type Context struct {
	Request      *types.CompletionRequest
	TrimmedLines []string
	WindowStart  int // 0-indexed
	WindowEnd    int // 0-indexed, exclusive
	CursorLine   int // 0-indexed within trimmed lines
	MaxLines     int // for streaming limit (0 = no limit)
	EndLineInc   int // 1-indexed inclusive end line, set by AnchorTruncation (0 = not set)
	Result       *openai.StreamResult

	// Streaming state
	CompletionRequest *openai.CompletionRequest // Built request for streaming

	// Stream context for FIM-style providers (implements engine.StreamContext)
	StreamOldLines []string // custom old lines for streaming diff (nil = not applicable)
	StreamBaseOff  int      // 0-indexed base offset in buffer
	FirstLinePfx   string   // prefix to prepend to first streamed line
	LastLineSfx    string   // suffix to append to last streamed line
}

// GetWindowStart returns the 0-indexed start offset of the trimmed window.
// Implements engine.TrimmedContext interface.
func (c *Context) GetWindowStart() int {
	return c.WindowStart
}

// GetTrimmedLines returns the trimmed lines sent to the model.
// Implements engine.TrimmedContext interface.
func (c *Context) GetTrimmedLines() []string {
	return c.TrimmedLines
}

// GetStreamOldLines returns custom old lines for streaming diff.
// Implements engine.StreamContext interface.
func (c *Context) GetStreamOldLines() []string {
	return c.StreamOldLines
}

// GetStreamBaseOffset returns the 0-indexed base offset in buffer.
// Implements engine.StreamContext interface.
func (c *Context) GetStreamBaseOffset() int {
	return c.StreamBaseOff
}

// TransformFirstLine prepends the stored prefix to the first streamed line.
// Implements engine.StreamContext interface.
func (c *Context) TransformFirstLine(line string) string {
	return c.FirstLinePfx + line
}

// TransformLastLine appends the stored suffix to the last streamed line.
// Implements engine.StreamContext interface.
func (c *Context) TransformLastLine(line string) string {
	return line + c.LastLineSfx
}

// Provider implements engine.Provider with a configurable pipeline
type Provider struct {
	Name           string
	Config         *types.ProviderConfig
	Client         Client
	StreamingType  StreamingType // Type of streaming (None, Lines, Tokens)
	Preprocessors  []Preprocessor
	PromptBuilder  PromptBuilder
	Postprocessors []Postprocessor
	Validators     []Validator        // Validators run on first line during streaming
	StopTokens     []string           // Stop tokens for streaming (provider-specific)
	DiffBuilder    DiffHistoryBuilder // Processes diff history for the prompt
	ContextLimits  engine.ContextLimits
}

// GetContextLimits implements engine.Provider
func (p *Provider) GetContextLimits() engine.ContextLimits {
	return p.ContextLimits.WithDefaults()
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("Provider.GetCompletion")()
	pctx := &Context{Request: req}

	for _, pre := range p.Preprocessors {
		if err := pre(p, pctx); err != nil {
			if errors.Is(err, ErrSkipCompletion) {
				return p.EmptyResponse(), nil
			}
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}

	completionReq := p.PromptBuilder(p, pctx)
	p.logRequest(completionReq, pctx.MaxLines)

	resp, err := p.Client.DoCompletion(ctx, completionReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}

	result := &openai.StreamResult{}
	if len(resp.Choices) > 0 {
		result.Text = resp.Choices[0].Text
		result.FinishReason = resp.Choices[0].FinishReason
	}
	pctx.Result = result
	p.logResponse(result)

	for _, post := range p.Postprocessors {
		if resp, done := post(p, pctx); done {
			return resp, nil
		}
	}

	return p.EmptyResponse(), nil
}

// EmptyResponse returns an empty completion response
func (p *Provider) EmptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
}

// BuildCompletion creates a completion response, returning empty if it's a no-op.
// startLine and endLineInc are 1-indexed.
func (p *Provider) BuildCompletion(ctx *Context, startLine, endLineInc int, lines []string) (*types.CompletionResponse, bool) {
	req := ctx.Request
	if endLineInc <= len(req.Lines) && IsNoOpReplacement(lines, req.Lines[startLine-1:endLineInc]) {
		return p.EmptyResponse(), true
	}

	completion := &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLineInc,
		Lines:      lines,
	}

	return &types.CompletionResponse{
		Completions:  []*types.Completion{completion},
		CursorTarget: nil,
	}, true
}

func (p *Provider) logRequest(req *openai.CompletionRequest, maxLines int) {
	logger.Debug("%s provider request:\n  URL: %s%s\n  Model: %s\n  Temperature: %.2f\n  MaxTokens: %d\n  MaxLines: %d\n  Prompt length: %d chars\n  Prompt:\n%s",
		p.Name,
		p.Config.ProviderURL,
		p.Config.CompletionPath,
		req.Model,
		req.Temperature,
		req.MaxTokens,
		maxLines,
		len(req.Prompt),
		req.Prompt)
}

func (p *Provider) logResponse(result *openai.StreamResult) {
	logger.Debug("%s provider response:\n  Text length: %d chars\n  FinishReason: %s\n  StoppedEarly: %v\n  Text:\n%s",
		p.Name,
		len(result.Text),
		result.FinishReason,
		result.StoppedEarly,
		result.Text)
}

// GetStreamingType returns the streaming type for this provider (implements engine.LineStreamProvider)
// Returns 0=none, 1=lines, 2=tokens to match engine.StreamingType* constants
func (p *Provider) GetStreamingType() int {
	return int(p.StreamingType)
}

// PrepareLineStream runs preprocessors, builds the prompt, and returns the stream.
// Returns (stream, providerContext, error). Implements engine.LineStreamProvider.
func (p *Provider) PrepareLineStream(ctx context.Context, req *types.CompletionRequest) (engine.LineStream, any, error) {
	defer logger.Trace("Provider.PrepareLineStream")()
	pctx := &Context{Request: req}

	for _, pre := range p.Preprocessors {
		if err := pre(p, pctx); err != nil {
			if errors.Is(err, ErrSkipCompletion) {
				return nil, pctx, ErrSkipCompletion
			}
			return nil, nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}

	completionReq := p.PromptBuilder(p, pctx)
	pctx.CompletionRequest = completionReq
	p.logRequest(completionReq, pctx.MaxLines)

	stream := p.Client.DoLineStream(ctx, completionReq, pctx.MaxLines, p.StopTokens)
	return stream, pctx, nil
}

// ValidateFirstLine runs validators on the first received line (implements engine.LineStreamProvider)
func (p *Provider) ValidateFirstLine(providerCtx any, firstLine string) error {
	pctx, ok := providerCtx.(*Context)
	if !ok {
		return fmt.Errorf("invalid provider context type")
	}

	for _, validator := range p.Validators {
		if err := validator(p, pctx, firstLine); err != nil {
			logger.Debug("%s: first line validation failed: %v", p.Name, err)
			return err
		}
	}
	return nil
}

// FinishLineStream runs postprocessors on the accumulated result (implements engine.LineStreamProvider)
func (p *Provider) FinishLineStream(providerCtx any, text string, finishReason string, stoppedEarly bool) (*types.CompletionResponse, error) {
	pctx, ok := providerCtx.(*Context)
	if !ok {
		return p.EmptyResponse(), fmt.Errorf("invalid provider context type")
	}

	pctx.Result = &openai.StreamResult{
		Text:         text,
		FinishReason: finishReason,
		StoppedEarly: stoppedEarly,
	}
	p.logResponse(pctx.Result)

	for _, post := range p.Postprocessors {
		if resp, done := post(p, pctx); done {
			return resp, nil
		}
	}

	return p.EmptyResponse(), nil
}

// PrepareTokenStream runs preprocessors, builds the prompt, and returns a token stream.
// Returns (stream, providerContext, error). Implements engine.TokenStreamProvider.
func (p *Provider) PrepareTokenStream(ctx context.Context, req *types.CompletionRequest) (engine.LineStream, any, error) {
	defer logger.Trace("Provider.PrepareTokenStream")()
	pctx := &Context{Request: req}

	for _, pre := range p.Preprocessors {
		if err := pre(p, pctx); err != nil {
			if errors.Is(err, ErrSkipCompletion) {
				return nil, pctx, ErrSkipCompletion
			}
			return nil, nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}

	completionReq := p.PromptBuilder(p, pctx)
	pctx.CompletionRequest = completionReq
	p.logRequest(completionReq, 0) // maxLines=0 for token streaming

	// DoTokenStream uses StopTokens and no maxChars limit (0)
	stream := p.Client.DoTokenStream(ctx, completionReq, 0, p.StopTokens)
	return stream, pctx, nil
}

// FinishTokenStream runs postprocessors on the final accumulated result (implements engine.TokenStreamProvider)
func (p *Provider) FinishTokenStream(providerCtx any, text string) (*types.CompletionResponse, error) {
	pctx, ok := providerCtx.(*Context)
	if !ok {
		return p.EmptyResponse(), fmt.Errorf("invalid provider context type")
	}

	pctx.Result = &openai.StreamResult{
		Text:         text,
		FinishReason: "stop",
		StoppedEarly: false,
	}
	p.logResponse(pctx.Result)

	for _, post := range p.Postprocessors {
		if resp, done := post(p, pctx); done {
			return resp, nil
		}
	}

	return p.EmptyResponse(), nil
}
