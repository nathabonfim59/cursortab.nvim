package text

import (
	"cursortab/logger"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// splitLines splits text by newline, removing the trailing empty element if present.
// This pairs with JoinLines which adds \n after each line:
// - "a\nb\n" -> ["a", "b"] (2 lines)
// - "a\n\n" -> ["a", ""] (2 lines, second is empty)
// - "a\nb" -> ["a", "b"] (2 lines, no trailing \n)
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ChangeType represents the type of change operation
type ChangeType int

const (
	ChangeDeletion ChangeType = iota
	ChangeAddition
	ChangeModification
	ChangeAppendChars
	ChangeDeleteChars
	ChangeReplaceChars
)

// String returns the string representation of ChangeType
func (ct ChangeType) String() string {
	switch ct {
	case ChangeDeletion:
		return "deletion"
	case ChangeAddition:
		return "addition"
	case ChangeModification:
		return "modification"
	case ChangeAppendChars:
		return "append_chars"
	case ChangeDeleteChars:
		return "delete_chars"
	case ChangeReplaceChars:
		return "replace_chars"
	default:
		return "unknown"
	}
}

// RenderHint returns the render hint string for character-level change types.
// Returns empty string for non-character-level changes.
func (ct ChangeType) RenderHint() string {
	switch ct {
	case ChangeAppendChars:
		return "append_chars"
	case ChangeDeleteChars:
		return "delete_chars"
	case ChangeReplaceChars:
		return "replace_chars"
	default:
		return ""
	}
}

// GroupType returns the group type string for rendering.
func (ct ChangeType) GroupType() string {
	switch ct {
	case ChangeAddition:
		return "addition"
	case ChangeDeletion:
		return "deletion"
	default:
		return "modification"
	}
}

// IsCharacterLevel returns true if this is a character-level change type.
func (ct ChangeType) IsCharacterLevel() bool {
	return ct == ChangeAppendChars || ct == ChangeDeleteChars || ct == ChangeReplaceChars
}

// LineChange represents a line-level change operation
type LineChange struct {
	Type       ChangeType
	OldLineNum int    // Position in old text (1-indexed), -1 if pure insertion
	NewLineNum int    // Position in new text (1-indexed), -1 if pure deletion
	Content    string // new content
	OldContent string // For modifications to compare changes
	ColStart   int    // Start column (0-based) for character-level changes
	ColEnd     int    // End column (0-based) for character-level changes
}

// LineMapping tracks correspondence between new and old line coordinates.
// This enables staging to work correctly when line counts differ (insertions/deletions).
type LineMapping struct {
	NewToOld []int // NewToOld[i] = old line num for new line i+1, or -1 if pure insertion
	OldToNew []int // OldToNew[i] = new line num for old line i+1, or -1 if deleted
}

// GetBufferLine calculates the buffer line for a change using coordinate mapping.
// mapKey is the change's key in the changes map (typically newLineNum).
// baseLineOffset is where the diff range starts in the buffer (1-indexed).
//
// For additions, the returned value is the insertion point (where virt_lines_above
// should render), NOT the anchor line. This means:
//   - If OldLineNum is set (anchor = line before insertion): returns anchor + 1
//   - If backward walk finds a mapped line: returns that line + 1
//   - If forward walk finds a mapped line: returns that line (insert before it)
func (m *LineMapping) GetBufferLine(change LineChange, mapKey, baseLineOffset int) int {
	isAddition := change.Type == ChangeAddition

	if change.OldLineNum > 0 {
		bufLine := change.OldLineNum + baseLineOffset - 1
		if isAddition {
			bufLine++
		}
		return bufLine
	}

	if m != nil && change.NewLineNum > 0 && change.NewLineNum <= len(m.NewToOld) {
		oldLine := m.NewToOld[change.NewLineNum-1]
		if oldLine > 0 {
			return oldLine + baseLineOffset - 1
		}
		if isAddition {
			// For additions, find the nearest mapped old line to determine
			// the buffer insertion point.
			// Forward walk: addition goes just before the next old line.
			for i := change.NewLineNum; i < len(m.NewToOld); i++ {
				if m.NewToOld[i] > 0 {
					return m.NewToOld[i] + baseLineOffset - 1
				}
			}
			// No forward match: addition is past the last old line.
			return len(m.OldToNew) + baseLineOffset
		}
		for i := change.NewLineNum - 2; i >= 0; i-- {
			if m.NewToOld[i] > 0 {
				return m.NewToOld[i] + baseLineOffset - 1
			}
		}
	}

	return mapKey + baseLineOffset - 1
}

// DiffResult contains all categorized change operations mapped by line number
type DiffResult struct {
	Changes      map[int]LineChange // Map of line number (1-indexed) to change operation
	LineMapping  *LineMapping       // Coordinate mapping between old and new line numbers
	OldLineCount int                // Number of lines in original text
	NewLineCount int                // Number of lines in new text
}

// =============================================================================
// DiffResult helper methods - SINGLE POINT for adding changes
// All invariants are enforced here, making it impossible to add invalid changes.
// =============================================================================

// addChange is the SINGLE ENTRY POINT for adding changes to the result.
// It enforces all invariants:
//   - Identical content is not a change (silently rejected)
//   - At least one line number must be valid
//   - Collision handling: deletion + addition at same line = modification
//
// oldLineNum: position in old text (1-indexed), -1 if pure insertion
// newLineNum: position in new text (1-indexed), -1 if pure deletion
// Returns true if the change was added, false if rejected.
func (r *DiffResult) addChange(oldLineNum, newLineNum int, oldContent, newContent string, changeType ChangeType, colStart, colEnd int) bool {
	// INVARIANT 1: Identical content is not a change
	// Exception: additions always have oldContent="" as placeholder, so we can't
	// compare content for them (adding an empty line is still a valid change)
	if oldContent == newContent && changeType != ChangeDeletion && changeType != ChangeAddition {
		return false
	}

	// INVARIANT 2: At least one line number must be valid
	if oldLineNum <= 0 && newLineNum <= 0 {
		return false
	}

	// Use newLineNum as the map key (primary coordinate for content extraction)
	// For deletions, use oldLineNum as the key since there's no new line
	mapKey := newLineNum
	if newLineNum <= 0 {
		mapKey = oldLineNum
	}

	// INVARIANT 3: Handle collisions
	if existing, exists := r.Changes[mapKey]; exists {
		// Deletion + Addition at same line = Modification
		// Note: For deletions, the deleted content is stored in Content field
		if existing.Type == ChangeDeletion && changeType == ChangeAddition {
			deletedContent := existing.Content // Deletion stores content in Content field
			// If the addition is empty, keep the deletion as-is
			if newContent == "" {
				return false
			}
			changeType, colStart, colEnd = categorizeLineChangeWithColumns(deletedContent, newContent)
			oldContent = deletedContent
			// Merge coordinates: take oldLineNum from existing deletion
			oldLineNum = existing.OldLineNum
		} else {
			// Other collisions: keep the existing change
			return false
		}
	}

	r.Changes[mapKey] = LineChange{
		Type:       changeType,
		OldLineNum: oldLineNum,
		NewLineNum: newLineNum,
		Content:    newContent,
		OldContent: oldContent,
		ColStart:   colStart,
		ColEnd:     colEnd,
	}
	return true
}

// addDeletion adds a deletion change with explicit coordinates.
// Note: For deletions, the deleted content is stored in the Content field (not OldContent).
// oldLineNum: position in old text (1-indexed, required)
// newLineNum: anchor point in new text (1-indexed), or -1 if no anchor
func (r *DiffResult) addDeletion(oldLineNum, newLineNum int, content string) bool {
	if oldLineNum <= 0 {
		return false
	}
	// Use oldLineNum as map key for deletions (no new line exists)
	mapKey := oldLineNum
	// For deletions, we bypass addChange to store content correctly
	// Deletions don't need identical-content check (deleting empty line is valid)
	if _, exists := r.Changes[mapKey]; exists {
		return false // Don't overwrite existing change
	}
	r.Changes[mapKey] = LineChange{
		Type:       ChangeDeletion,
		OldLineNum: oldLineNum,
		NewLineNum: newLineNum, // -1 or anchor point
		Content:    content,    // Deleted content goes in Content field
	}
	return true
}

// addAddition adds an addition change with explicit coordinates.
// oldLineNum: anchor point in old text (1-indexed), or -1 if no anchor
// newLineNum: position in new text (1-indexed, required)
func (r *DiffResult) addAddition(oldLineNum, newLineNum int, content string) bool {
	return r.addChange(oldLineNum, newLineNum, "", content, ChangeAddition, 0, 0)
}

// addModification adds a modification change with explicit coordinates,
// auto-categorizing the change type based on content differences.
// oldLineNum: position in old text (1-indexed)
// newLineNum: position in new text (1-indexed)
func (r *DiffResult) addModification(oldLineNum, newLineNum int, oldContent, newContent string) bool {
	changeType, colStart, colEnd := categorizeLineChangeWithColumns(oldContent, newContent)
	return r.addChange(oldLineNum, newLineNum, oldContent, newContent, changeType, colStart, colEnd)
}

// ComputeDiff computes and categorizes line-level changes between two texts
func ComputeDiff(text1, text2 string) *DiffResult {
	defer logger.Trace("text.ComputeDiff")()
	// Count lines in both texts
	oldLines := splitLines(text1)
	newLines := splitLines(text2)
	oldLineCount := len(oldLines)
	newLineCount := len(newLines)

	result := &DiffResult{
		Changes:      make(map[int]LineChange),
		OldLineCount: oldLineCount,
		NewLineCount: newLineCount,
	}

	// When old text is a single empty line, the diff library may match it as
	// "equal" to an interior empty line in the new text, producing pure additions
	// instead of a modification at line 1. Force a delete+insert so the collision
	// logic in addChange merges them into a modification (append_chars).
	if oldLineCount == 1 && oldLines[0] == "" && newLineCount > 0 {
		lineDiffs := []diffmatchpatch.Diff{
			{Type: diffmatchpatch.DiffDelete, Text: text1},
			{Type: diffmatchpatch.DiffInsert, Text: text2},
		}
		result.LineMapping = processLineDiffsWithMapping(lineDiffs, result, oldLineCount, newLineCount)
		return result
	}

	dmp := diffmatchpatch.New()
	chars1, chars2, lineArray := dmp.DiffLinesToChars(text1, text2)
	diffs := dmp.DiffMain(chars1, chars2, false)
	lineDiffs := dmp.DiffCharsToLines(diffs, lineArray)

	// Build line mapping and process diffs
	result.LineMapping = processLineDiffsWithMapping(lineDiffs, result, oldLineCount, newLineCount)

	return result
}

// LineSimilarity computes a similarity score between two lines (0.0 to 1.0)
// using Levenshtein ratio: 1 - (levenshtein_distance / max_length)
// Higher score means more similar. Empty lines have 0 similarity with non-empty lines.
func LineSimilarity(line1, line2 string) float64 {
	// Empty lines
	if line1 == "" && line2 == "" {
		return 1.0
	}
	if line1 == "" || line2 == "" {
		return 0.0
	}

	// Use Levenshtein ratio for intuitive similarity scoring
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(line1, line2, false)
	levenshteinDist := dmp.DiffLevenshtein(diffs)

	maxLen := max(len(line1), len(line2))
	if maxLen == 0 {
		return 0.0
	}

	return 1.0 - float64(levenshteinDist)/float64(maxLen)
}

// matchScore computes a match score between two lines, combining prefix matching
// and Levenshtein similarity into a single score. Prefix matches get a bonus to
// ensure "partial" → "partial content completed" always wins over fuzzy matches.
func matchScore(oldLine, newLine string) float64 {
	if oldLine == "" || newLine == "" {
		return 0.0
	}
	trimmed := strings.TrimRight(oldLine, " \t")
	if trimmed != "" && strings.HasPrefix(newLine, trimmed) {
		return 1.0
	}
	return LineSimilarity(oldLine, newLine)
}

// findBestMatch finds the best matching line in insertedLines for the given deletedLine.
// Returns the index of the best match and its score.
func findBestMatch(deletedLine string, insertedLines []string, usedInserts map[int]bool) (int, float64) {
	bestIdx := -1
	bestScore := 0.0

	for i, insertedLine := range insertedLines {
		if usedInserts[i] {
			continue
		}
		score := matchScore(deletedLine, insertedLine)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	return bestIdx, bestScore
}

// processLineDiffsWithMapping processes line-level diffs and builds the coordinate mapping.
// Returns the LineMapping that tracks correspondence between old and new line numbers.
func processLineDiffsWithMapping(lineDiffs []diffmatchpatch.Diff, result *DiffResult, oldLineCount, newLineCount int) *LineMapping {
	// Initialize mapping arrays with -1 (unmapped)
	newToOld := make([]int, newLineCount)
	oldToNew := make([]int, oldLineCount)
	for i := range newToOld {
		newToOld[i] = -1
	}
	for i := range oldToNew {
		oldToNew[i] = -1
	}

	oldLineNum := 0 // 0-indexed counter
	newLineNum := 0 // 0-indexed counter
	i := 0

	for i < len(lineDiffs) {
		diff := lineDiffs[i]
		lines := splitLines(diff.Text)

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			// Equal lines map 1:1
			for j := range len(lines) {
				if newLineNum+j < newLineCount {
					newToOld[newLineNum+j] = oldLineNum + j + 1 // 1-indexed
				}
				if oldLineNum+j < oldLineCount {
					oldToNew[oldLineNum+j] = newLineNum + j + 1 // 1-indexed
				}
			}
			oldLineNum += len(lines)
			newLineNum += len(lines)
			i++

		case diffmatchpatch.DiffDelete:
			// Check if this is followed by an insert - potential modification
			if i+1 < len(lineDiffs) && lineDiffs[i+1].Type == diffmatchpatch.DiffInsert {
				// This is a delete followed by insert - treat as modification(s)
				insertLines := splitLines(lineDiffs[i+1].Text)

				// Build mapping for the modification region
				handleModificationsWithMapping(lines, insertLines, oldLineNum, newLineNum,
					oldLineCount, newLineCount, newToOld, oldToNew, result)

				oldLineNum += len(lines)
				newLineNum += len(insertLines)
				i += 2 // Skip both delete and insert
			} else {
				// Pure deletion - deleted lines have no new correspondence
				// oldToNew already -1, add deletion changes
				for j, line := range lines {
					oldIdx := oldLineNum + j
					// Find the anchor point in new text (the line before deletion, or -1)
					anchorNew := -1
					if newLineNum > 0 {
						anchorNew = newLineNum
					} else if newLineCount > 0 {
						anchorNew = 1
					}
					result.addDeletion(oldIdx+1, anchorNew, line)
				}
				oldLineNum += len(lines)
				i++
			}

		case diffmatchpatch.DiffInsert:
			// Pure addition (not preceded by delete)
			// newToOld already -1, add addition changes
			for j, line := range lines {
				newIdx := newLineNum + j
				// Find the anchor point in old text (the line before insertion)
				anchorOld := -1
				if oldLineNum > 0 {
					anchorOld = oldLineNum // Point to line before (for buffer coordinate calculation)
				}
				result.addAddition(anchorOld, newIdx+1, line)
			}
			newLineNum += len(lines)
			i++
		}
	}

	return &LineMapping{
		NewToOld: newToOld,
		OldToNew: oldToNew,
	}
}

// handleModificationsWithMapping processes delete+insert pairs as modifications
// and updates the coordinate mapping accordingly.
func handleModificationsWithMapping(deletedLines, insertedLines []string,
	oldLineStart, newLineStart int,
	oldLineCount, newLineCount int,
	newToOld, oldToNew []int,
	result *DiffResult) {

	// If we have equal number of lines, treat each pair as a modification with 1:1 mapping
	if len(deletedLines) == len(insertedLines) {
		for j := range len(deletedLines) {
			oldIdx := oldLineStart + j
			newIdx := newLineStart + j

			// Update mapping - these lines correspond
			if newIdx < newLineCount {
				newToOld[newIdx] = oldIdx + 1 // 1-indexed
			}
			if oldIdx < oldLineCount {
				oldToNew[oldIdx] = newIdx + 1 // 1-indexed
			}

			if deletedLines[j] != "" && insertedLines[j] != "" {
				// Both non-empty: modification
				result.addModification(oldIdx+1, newIdx+1, deletedLines[j], insertedLines[j])
			} else if deletedLines[j] != "" {
				// Only old has content: deletion
				result.addDeletion(oldIdx+1, newIdx+1, deletedLines[j])
			} else if insertedLines[j] != "" {
				// Empty line filled with content: modification (categorizes as append_chars)
				result.addModification(oldIdx+1, newIdx+1, "", insertedLines[j])
			}
		}
		return
	}

	// Unequal number of lines — single-pass greedy matching using combined scoring
	// (prefix match + Levenshtein similarity), then emit modifications/deletions/additions.
	usedInserts := make(map[int]bool)
	matches := make(map[int]int) // deleted index → inserted index

	for i, deletedLine := range deletedLines {
		if deletedLine == "" || strings.TrimSpace(deletedLine) == "" {
			continue
		}
		bestIdx, bestScore := findBestMatch(deletedLine, insertedLines, usedInserts)
		if bestIdx != -1 && bestScore >= SimilarityThreshold {
			matches[i] = bestIdx
			usedInserts[bestIdx] = true
		}
	}

	// Emit matched pairs as modifications
	for delIdx, insIdx := range matches {
		oldIdx := oldLineStart + delIdx
		newIdx := newLineStart + insIdx
		if newIdx < newLineCount {
			newToOld[newIdx] = oldIdx + 1
		}
		if oldIdx < oldLineCount {
			oldToNew[oldIdx] = newIdx + 1
		}
		result.addModification(oldIdx+1, newIdx+1, deletedLines[delIdx], insertedLines[insIdx])
	}

	// Emit unmatched deletions
	for i, deletedLine := range deletedLines {
		if _, matched := matches[i]; matched {
			continue
		}
		oldIdx := oldLineStart + i
		anchorNew := -1
		if newLineStart > 0 {
			anchorNew = newLineStart
		}
		result.addDeletion(oldIdx+1, anchorNew, deletedLine)
	}

	// Emit unmatched additions, anchored to the nearest preceding matched old line
	for i, insertedLine := range insertedLines {
		if usedInserts[i] {
			continue
		}
		newIdx := newLineStart + i
		anchorOld := -1
		for delIdx, insIdx := range matches {
			if insIdx < i {
				candidate := oldLineStart + delIdx + 1
				if candidate > anchorOld {
					anchorOld = candidate
				}
			}
		}
		result.addAddition(anchorOld, newIdx+1, insertedLine)
	}
}

// categorizeLineChangeWithColumns determines the type of change between two lines
// using common prefix/suffix analysis to find the single contiguous changed span.
func categorizeLineChangeWithColumns(oldLine, newLine string) (ChangeType, int, int) {
	if oldLine == "" && newLine != "" {
		return ChangeAppendChars, 0, len(newLine)
	}

	if strings.HasPrefix(newLine, oldLine) {
		return ChangeAppendChars, len(oldLine), len(newLine)
	}

	prefixLen := 0
	minLen := min(len(oldLine), len(newLine))
	for prefixLen < minLen && oldLine[prefixLen] == newLine[prefixLen] {
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < minLen-prefixLen &&
		oldLine[len(oldLine)-1-suffixLen] == newLine[len(newLine)-1-suffixLen] {
		suffixLen++
	}

	oldMiddle := len(oldLine) - prefixLen - suffixLen
	newMiddle := len(newLine) - prefixLen - suffixLen

	if prefixLen == 0 && suffixLen == 0 {
		return ChangeModification, 0, 0
	}

	if newMiddle == 0 && oldMiddle > 0 {
		return ChangeDeleteChars, prefixLen, prefixLen + oldMiddle
	}

	// ReplaceChars for localized changes within the line.
	if newMiddle > 0 {
		changed := max(oldMiddle, newMiddle)
		if changed <= MaxReplaceCharsSpan {
			return ChangeReplaceChars, prefixLen, prefixLen + newMiddle
		}
		return ChangeModification, 0, 0
	}

	return ChangeModification, 0, 0
}

// FindFirstChangedLine compares old lines with new lines and returns the first line number (1-indexed)
// where they differ. Returns 0 if no differences found.
// The baseLineOffset is added to the result to convert from relative to absolute line numbers.
func FindFirstChangedLine(oldLines, newLines []string, baseLineOffset int) int {
	// Quick path: find first differing line by direct comparison
	minLen := min(len(oldLines), len(newLines))

	for i := range minLen {
		if oldLines[i] != newLines[i] {
			return i + 1 + baseLineOffset // 1-indexed + offset
		}
	}

	// If lengths differ, the first "extra" line is a change
	if len(oldLines) != len(newLines) {
		return minLen + 1 + baseLineOffset
	}

	// No differences found
	return 0
}
