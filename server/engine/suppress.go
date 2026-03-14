package engine

import (
	"cursortab/logger"
	"cursortab/types"
	"regexp"
)

// inertSuffixPattern matches cursor suffixes where insertion-only completions
// are still useful: whitespace, closing brackets, trailing punctuation.
// Matches Copilot's heuristic: /^\s*[)>}\]"'`]*\s*[:{;,]?\s*$/
var inertSuffixPattern = regexp.MustCompile(`^\s*[)>}\]"'` + "`" + `]*\s*[:{;,]?\s*$`)

// consecutiveDeletionThreshold is the number of consecutive deletion actions
// after which completions are re-enabled (user is rewriting, not correcting).
const consecutiveDeletionThreshold = 3

// shouldSuppressCompletion returns true if the completion request should be
// skipped based on recent user actions and cursor context.
func (e *Engine) shouldSuppressCompletion() bool {
	if e.suppressForSingleDeletion() {
		logger.Debug("suppress: single deletion")
		return true
	}
	if e.suppressForMidLine() {
		logger.Debug("suppress: mid-line cursor (insertion-only provider)")
		return true
	}
	return false
}

// suppressForSingleDeletion returns true if the last action was a single
// deletion (typo correction) without a streak of deletions (rewrite).
func (e *Engine) suppressForSingleDeletion() bool {
	if len(e.userActions) == 0 {
		return false
	}

	last := e.userActions[len(e.userActions)-1]
	if !isDeletion(last.ActionType) {
		return false
	}

	// Count consecutive deletions from the end
	consecutive := 0
	for i := len(e.userActions) - 1; i >= 0; i-- {
		if isDeletion(e.userActions[i].ActionType) {
			consecutive++
		} else {
			break
		}
	}

	// A streak of deletions means the user is rewriting → allow completions
	return consecutive < consecutiveDeletionThreshold
}

// suppressForMidLine returns true if the cursor is in the middle of a line
// with meaningful code to the right, and the provider is insertion-only.
func (e *Engine) suppressForMidLine() bool {
	if !e.config.InsertionOnly {
		return false
	}

	lines := e.buffer.Lines()
	row := e.buffer.Row() // 1-indexed
	col := e.buffer.Col() // 0-indexed

	if row < 1 || row > len(lines) {
		return false
	}

	line := lines[row-1]
	if col >= len(line) {
		return false // cursor at end of line
	}

	suffix := line[col:]
	return !inertSuffixPattern.MatchString(suffix)
}

func isDeletion(action types.UserActionType) bool {
	return action == types.ActionDeleteChar || action == types.ActionDeleteSelection
}
