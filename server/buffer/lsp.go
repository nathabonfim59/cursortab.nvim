package buffer

import (
	_ "embed"
	"fmt"

	"github.com/neovim/go-client/nvim"
)

// LspClientInfo contains information about an attached LSP client
type LspClientInfo struct {
	ID int
}

// GetLspClient returns info about the Copilot LSP client if attached to the current buffer
func (b *NvimBuffer) GetLspClient() (*LspClientInfo, error) {
	if b.client == nil {
		return nil, fmt.Errorf("nvim client not set")
	}

	var result []map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(`return require('cursortab.lsp').get_lsp_client({'copilot', 'GitHub Copilot'})`, &result, nil)

	if err := batch.Execute(); err != nil {
		return nil, fmt.Errorf("failed to get Copilot client: %w", err)
	}

	if len(result) == 0 {
		return nil, nil // No Copilot client attached
	}

	return &LspClientInfo{
		ID: getNumber(result[0], "id"),
	}, nil
}

// SendLspDidFocus sends textDocument/didFocus notification to Copilot LSP
func (b *NvimBuffer) SendLspDidFocus(uri string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	batch.ExecLua(`
		return require('cursortab.lsp').send_lsp_event(
			{ 'copilot', 'GitHub Copilot' },
			'textDocument/didFocus',
			{ textDocument = { uri = ... } }
		)
	`, nil, uri)

	return batch.Execute()
}

// SendLspNESRequest sends textDocument/copilotInlineEdit request and delivers response via registered handler
func (b *NvimBuffer) SendLspNESRequest(reqID int64, uri string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// Get the channel ID for RPC communication back to Go
	chanID := b.client.ChannelID()

	batch := b.client.NewBatch()

	batch.ExecLua(`
		local chanID, reqID, uri = ...
		require('cursortab.lsp').send_nes_request(
			{'copilot', 'GitHub Copilot'},
			{ chanID = chanID, reqID = reqID, uri = uri }
		)
	`, nil, chanID, reqID, uri)

	return batch.Execute()
}

// RegisterLspHandler registers a handler for Copilot NES responses
func (b *NvimBuffer) RegisterLspHandler(handler func(reqID int64, editsJSON string, errMsg string)) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	return b.client.RegisterHandler("cursortab_copilot_response", func(_ *nvim.Nvim, reqID int64, editsJSON string, errMsg string) {
		handler(reqID, editsJSON, errMsg)
	})
}
