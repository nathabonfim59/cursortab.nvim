package text

import (
	"cursortab/types"
	"sort"
	"strings"
)

// Stage represents a single stage of changes to apply
type Stage struct {
	BufferStart  int                           // 1-indexed buffer coordinate
	BufferEnd    int                           // 1-indexed, inclusive
	Lines        []string                      // New content for this stage
	Changes      map[int]LineChange            // Changes keyed by line num relative to stage
	Groups       []*Group                      // Pre-computed groups for rendering
	CursorLine   int                           // Cursor position (1-indexed, relative to stage)
	CursorCol    int                           // Cursor column (0-indexed)
	CursorTarget *types.CursorPredictionTarget // Navigation target
	IsLastStage  bool

	// Unexported fields for construction (not serialized)
	rawChanges map[int]LineChange // Original changes with absolute line nums
	startLine  int                // First change line (absolute, 1-indexed)
	endLine    int                // Last change line (absolute, 1-indexed)
}

// StagingResult contains the result of CreateStages
type StagingResult struct {
	Stages               []*Stage
	FirstNeedsNavigation bool
}

// StagedCompletion holds the queue of pending stages
type StagedCompletion struct {
	Stages           []*Stage
	CurrentIdx       int
	SourcePath       string
	CumulativeOffset int // Tracks line count drift after each stage accept (for unequal line counts)
}

// StagingParams holds all parameters for CreateStages
type StagingParams struct {
	Diff               *DiffResult
	CursorRow          int // 1-indexed buffer coordinate
	CursorCol          int // 0-indexed
	ViewportTop        int // 1-indexed buffer coordinate
	ViewportBottom     int // 1-indexed buffer coordinate
	BaseLineOffset     int // Where the diff range starts in the buffer (1-indexed)
	ProximityThreshold int // Max gap between changes to be in same stage
	MaxLines           int // Max lines per stage (0 to disable)
	FilePath           string
	NewLines           []string // New content lines for extracting stage content
	OldLines           []string // Old content lines for extracting old content in groups
}

// CreateStages is the main entry point for creating stages from a diff result.
// Always returns stages (at least 1 stage for non-empty changes).
func CreateStages(p *StagingParams) *StagingResult {
	diff := p.Diff
	if len(diff.Changes) == 0 {
		return nil
	}

	// Step 1: Partition changes by viewport visibility
	var inViewChanges, outViewChanges []int
	for lineNum, change := range diff.Changes {
		bufferLine := diff.LineMapping.GetBufferLine(change, lineNum, p.BaseLineOffset)

		isVisible := p.ViewportTop == 0 && p.ViewportBottom == 0 ||
			(bufferLine >= p.ViewportTop && bufferLine <= p.ViewportBottom)

		if isVisible {
			inViewChanges = append(inViewChanges, lineNum)
		} else {
			outViewChanges = append(outViewChanges, lineNum)
		}
	}

	sort.Ints(inViewChanges)
	sort.Ints(outViewChanges)

	// Step 2: Group changes into partial stages
	inViewStages := groupChangesIntoStages(diff, inViewChanges, p.ProximityThreshold, p.MaxLines, p.BaseLineOffset)
	outViewStages := groupChangesIntoStages(diff, outViewChanges, p.ProximityThreshold, p.MaxLines, p.BaseLineOffset)
	allStages := append(inViewStages, outViewStages...)

	if len(allStages) == 0 {
		return nil
	}

	// Step 3: Sort stages by cursor distance
	sort.SliceStable(allStages, func(i, j int) bool {
		distI := stageDistanceFromCursor(allStages[i], p.CursorRow)
		distJ := stageDistanceFromCursor(allStages[j], p.CursorRow)
		if distI != distJ {
			return distI < distJ
		}
		return allStages[i].startLine < allStages[j].startLine
	})

	// Step 4: Finalize stages (content, cursor targets)
	finalizeStages(allStages, p.NewLines, p.FilePath, p.BaseLineOffset, diff, p.CursorRow, p.CursorCol)

	// Step 5: Check if first stage needs navigation UI
	firstNeedsNav := StageNeedsNavigation(
		allStages[0], p.CursorRow, p.ViewportTop, p.ViewportBottom, p.ProximityThreshold,
	)

	return &StagingResult{
		Stages:               allStages,
		FirstNeedsNavigation: firstNeedsNav,
	}
}

// groupChangesIntoStages groups sorted line numbers into partial Stage structs based on proximity
// and stage line limits. The returned stages have rawChanges, startLine, endLine, BufferStart, and BufferEnd
// populated. Other fields are left as zero values to be filled by finalizeStages.
func groupChangesIntoStages(diff *DiffResult, lineNumbers []int, proximityThreshold int, maxLines int, baseLineOffset int) []*Stage {
	if len(lineNumbers) == 0 {
		return nil
	}

	var stages []*Stage
	var currentStage *Stage

	for _, lineNum := range lineNumbers {
		change := diff.Changes[lineNum]
		endLine := lineNum

		if currentStage == nil {
			currentStage = &Stage{
				startLine:  lineNum,
				endLine:    endLine,
				rawChanges: make(map[int]LineChange),
			}
			currentStage.rawChanges[lineNum] = change
		} else {
			gap := lineNum - currentStage.endLine
			// Check both proximity threshold and stage line limit
			stageLineCount := currentStage.endLine - currentStage.startLine + 1
			exceedsMaxLines := maxLines > 0 && stageLineCount >= maxLines
			if gap <= proximityThreshold && !exceedsMaxLines {
				currentStage.rawChanges[lineNum] = change
				if endLine > currentStage.endLine {
					currentStage.endLine = endLine
				}
			} else {
				// Compute buffer range before appending
				currentStage.BufferStart, currentStage.BufferEnd = getStageBufferRange(currentStage, baseLineOffset, diff, nil)
				stages = append(stages, currentStage)
				currentStage = &Stage{
					startLine:  lineNum,
					endLine:    endLine,
					rawChanges: make(map[int]LineChange),
				}
				currentStage.rawChanges[lineNum] = change
			}
		}
	}

	if currentStage != nil {
		currentStage.BufferStart, currentStage.BufferEnd = getStageBufferRange(currentStage, baseLineOffset, diff, nil)
		stages = append(stages, currentStage)
	}

	return stages
}

// StageNeedsNavigation determines if a stage requires cursor prediction UI.
// Returns true if the stage is outside viewport or far from cursor.
func StageNeedsNavigation(stage *Stage, cursorRow, viewportTop, viewportBottom, distThreshold int) bool {
	// Check distance first - if within threshold, no navigation needed.
	// This handles cases like additions at end of file where BufferStart may be
	// beyond the viewport but the stage is still close to the cursor.
	distance := stageDistanceFromCursor(stage, cursorRow)
	if distance <= distThreshold {
		return false
	}

	// Check viewport bounds for stages that are far from cursor
	if viewportTop > 0 && viewportBottom > 0 {
		entirelyOutside := stage.BufferEnd < viewportTop || stage.BufferStart > viewportBottom
		if entirelyOutside {
			return true
		}
	}

	return true // distance > distThreshold
}

// stageDistanceFromCursor calculates the minimum distance from cursor to a stage.
func stageDistanceFromCursor(stage *Stage, cursorRow int) int {
	if cursorRow >= stage.BufferStart && cursorRow <= stage.BufferEnd {
		return 0
	}
	if cursorRow < stage.BufferStart {
		return stage.BufferStart - cursorRow
	}
	return cursorRow - stage.BufferEnd
}

// getStageBufferRange computes the buffer line range (old coordinates) for a
// stage and optionally populates lineNum→bufferLine mappings for UI rendering.
//
// The returned range [bufStart, bufEnd] defines which old buffer lines get
// replaced by Stage.Lines via SetBufferLines. For pure insertions (all
// additions from the same anchor), bufStart == bufEnd and no old lines are
// replaced.
//
// For additions, OldLineNum is the anchor (line before insertion). In mixed
// stages the anchor extends the old range so that the replacement captures
// the correct new-file content between surrounding unchanged lines.
func getStageBufferRange(stage *Stage, baseLineOffset int, diff *DiffResult, bufferLines map[int]int) (int, int) {
	// Populate buffer line mappings for UI group positioning
	if bufferLines != nil {
		for lineNum, change := range stage.rawChanges {
			bufferLines[lineNum] = diff.LineMapping.GetBufferLine(change, lineNum, baseLineOffset)
		}
	}

	minOldNonAdd := -1
	maxOldNonAdd := -1
	minAnchor := -1
	maxAnchor := -1
	hasNonAdditions := false
	hasAdditions := false

	for _, change := range stage.rawChanges {
		if change.Type == ChangeAddition {
			hasAdditions = true
			if change.OldLineNum > 0 {
				if minAnchor == -1 || change.OldLineNum < minAnchor {
					minAnchor = change.OldLineNum
				}
				if change.OldLineNum > maxAnchor {
					maxAnchor = change.OldLineNum
				}
			}
		} else {
			hasNonAdditions = true
			if change.OldLineNum > 0 {
				if minOldNonAdd == -1 || change.OldLineNum < minOldNonAdd {
					minOldNonAdd = change.OldLineNum
				}
				if change.OldLineNum > maxOldNonAdd {
					maxOldNonAdd = change.OldLineNum
				}
			}
		}
	}

	var oldStart, oldEnd int

	if !hasNonAdditions && hasAdditions {
		// Pure additions: anchor is the old line before insertion
		if minAnchor == maxAnchor {
			// All anchored at the same line → pure insertion point
			oldStart = minAnchor + 1
			oldEnd = minAnchor + 1
		} else {
			// Different anchors → must replace old lines between them
			oldStart = minAnchor + 1
			oldEnd = maxAnchor
		}
	} else if !hasAdditions {
		// No additions: range covers modified/deleted old lines
		oldStart = minOldNonAdd
		oldEnd = maxOldNonAdd
	} else {
		// Mixed: start with non-addition range, extend for addition anchors
		oldStart = minOldNonAdd
		oldEnd = maxOldNonAdd
		for _, change := range stage.rawChanges {
			if change.Type == ChangeAddition && change.OldLineNum > 0 {
				if change.OldLineNum >= oldEnd {
					oldEnd = change.OldLineNum
				}
				if change.OldLineNum+1 < oldStart {
					oldStart = change.OldLineNum + 1
				}
			}
		}
	}

	// For anchorless additions (OldLineNum=-1), derive the insertion
	// point from the LineMapping using the change's NewLineNum.
	if oldStart <= 0 && diff.LineMapping != nil {
		for _, change := range stage.rawChanges {
			if change.NewLineNum > 0 && change.NewLineNum <= len(diff.LineMapping.NewToOld) {
				// Walk forward from NewLineNum to find the next mapped old line
				for i := change.NewLineNum - 1; i < len(diff.LineMapping.NewToOld); i++ {
					if diff.LineMapping.NewToOld[i] > 0 {
						pos := diff.LineMapping.NewToOld[i]
						if oldStart <= 0 || pos < oldStart {
							oldStart = pos
						}
						break
					}
				}
				// If no forward match, insertion is past last old line
				if oldStart <= 0 {
					oldStart = len(diff.LineMapping.OldToNew) + 1
				}
			}
		}
	}
	if oldEnd <= 0 {
		oldEnd = oldStart
	}
	if oldStart <= 0 {
		oldStart = stage.startLine
	}
	if oldEnd <= 0 {
		oldEnd = stage.endLine
	}

	return oldStart + baseLineOffset - 1, oldEnd + baseLineOffset - 1
}

// getStageNewLineRange derives the new-file line range from the old-file range
// using the LineMapping. This ensures the ranges are aligned: replacing
// old[bufStart:bufEnd] with new[newStart:newEnd] produces the correct result.
//
// For pure deletions the new range is empty (newStart > newEnd), producing
// an empty Stage.Lines that deletes the old lines.
func getStageNewLineRange(bufStart, bufEnd, baseLineOffset int, isPureInsertion bool, mapping *LineMapping) (int, int) {
	if mapping == nil {
		return bufStart - baseLineOffset + 1, bufEnd - baseLineOffset + 1
	}

	oldRelStart := bufStart - baseLineOffset + 1
	oldRelEnd := bufEnd - baseLineOffset + 1

	// For pure insertion, no old lines are replaced: the suffix starts at
	// oldRelStart (the insertion point). For replacement, old lines
	// [oldRelStart, oldRelEnd] are consumed, so the suffix starts at
	// oldRelEnd + 1.
	suffixOldLine := oldRelEnd + 1
	if isPureInsertion {
		suffixOldLine = oldRelStart
	}

	// newStart = first new line after the last preserved old line before the range
	var newStart int
	if oldRelStart > 1 {
		prev := -1
		for i := min(oldRelStart-2, len(mapping.OldToNew)-1); i >= 0; i-- {
			if mapping.OldToNew[i] != -1 {
				prev = mapping.OldToNew[i]
				break
			}
		}
		if prev == -1 {
			newStart = 1
		} else {
			newStart = prev + 1
		}
	} else {
		newStart = 1
	}

	// newEnd = last new line before the first preserved old line after the range
	var newEnd int
	suffixIdx := suffixOldLine - 1 // 0-indexed into OldToNew
	if suffixIdx < len(mapping.OldToNew) {
		next := -1
		for i := suffixIdx; i < len(mapping.OldToNew); i++ {
			if mapping.OldToNew[i] != -1 {
				next = mapping.OldToNew[i]
				break
			}
		}
		if next == -1 {
			newEnd = len(mapping.NewToOld)
		} else {
			newEnd = next - 1
		}
	} else {
		newEnd = len(mapping.NewToOld)
	}

	return newStart, newEnd
}

// isSameAnchorAdditions returns true if all changes are additions anchored
// at the same old line. This is the condition for a pure insertion where
// SetBufferLines inserts without replacing any existing lines.
func isSameAnchorAdditions(changes map[int]LineChange) bool {
	anchor := -1
	for _, c := range changes {
		if c.Type != ChangeAddition {
			return false
		}
		if anchor == -1 {
			anchor = c.OldLineNum
		} else if c.OldLineNum != anchor {
			return false
		}
	}
	return anchor >= 0
}

// finalizeStages populates the remaining fields of partial stages.
// It extracts content, remaps changes to relative line numbers, computes groups,
// and sets cursor targets based on sort order.
func finalizeStages(stages []*Stage, newLines []string, filePath string, baseLineOffset int, diff *DiffResult, cursorRow, cursorCol int) {
	for i, stage := range stages {
		isLastStage := i == len(stages)-1

		// Get buffer line mappings for this stage
		lineNumToBufferLine := make(map[int]int)
		getStageBufferRange(stage, baseLineOffset, diff, lineNumToBufferLine)

		// Derive new line range from old range via LineMapping
		isPureInsert := stage.BufferStart == stage.BufferEnd && isSameAnchorAdditions(stage.rawChanges)
		newStartLine, newEndLine := getStageNewLineRange(stage.BufferStart, stage.BufferEnd, baseLineOffset, isPureInsert, diff.LineMapping)

		// Extract the new content using new coordinates
		var stageLines []string
		for j := newStartLine; j <= newEndLine && j-1 < len(newLines); j++ {
			if j > 0 {
				stageLines = append(stageLines, newLines[j-1])
			}
		}

		// Extract old content for modifications and create remapped changes
		stageOldLines := make([]string, len(stageLines))
		remappedChanges := make(map[int]LineChange)
		relativeToBufferLine := make(map[int]int)

		// Deletions use old-coordinate space; non-deletions use new-coordinate space.
		// When both are present, shift all deletions beyond the non-deletion range so
		// their relative lines never overlap. Non-deletions occupy [1..nCount] and
		// deletions occupy [nCount+1..], preserving inter-deletion gaps for grouping.
		hasNonDeletions := false
		for _, change := range stage.rawChanges {
			if change.Type != ChangeDeletion {
				hasNonDeletions = true
				break
			}
		}
		nCount := 0
		if hasNonDeletions {
			nCount = len(stageLines)
		}

		for lineNum, change := range stage.rawChanges {
			var relativeLine int
			if change.Type == ChangeDeletion {
				// Position relative to stage.startLine to preserve gaps between
				// adjacent and non-adjacent deletions (affects grouping). When
				// non-deletions are present, shift beyond the non-deletion range.
				rel := lineNum - stage.startLine + 1
				if nCount > 0 {
					rel = nCount + rel
				}
				relativeLine = rel
			} else {
				newLineNum := lineNum
				if change.NewLineNum > 0 {
					newLineNum = change.NewLineNum
				}
				relativeLine = newLineNum - newStartLine + 1
			}
			relativeIdx := relativeLine - 1

			if relativeIdx >= 0 && relativeIdx < len(stageOldLines) {
				stageOldLines[relativeIdx] = change.OldContent
			}

			if relativeLine > 0 && (relativeLine <= len(stageLines) || change.Type == ChangeDeletion) {
				// Use pre-computed buffer line from getStageBufferRange
				relativeToBufferLine[relativeLine] = lineNumToBufferLine[lineNum]

				remappedChange := change
				remappedChange.NewLineNum = relativeLine
				remappedChanges[relativeLine] = remappedChange
			}
		}

		// Compute groups, set BufferLine, validate render hints, and compute cursor position
		ctx := &StageContext{
			BufferStart:         stage.BufferStart,
			CursorRow:           cursorRow,
			CursorCol:           cursorCol,
			LineNumToBufferLine: relativeToBufferLine,
		}
		groups, targetCursorLine, targetCursorCol := FinalizeStageGroups(remappedChanges, stageLines, ctx)

		// Create cursor target
		var cursorTarget *types.CursorPredictionTarget
		if isLastStage {
			// For last stage, cursor target points to end of NEW content,
			// not the old buffer end. This is important when additions extend
			// beyond the original buffer.
			newEndLine := stage.BufferStart + len(stageLines) - 1
			cursorTarget = &types.CursorPredictionTarget{
				RelativePath:    filePath,
				LineNumber:      int32(newEndLine),
				ShouldRetrigger: true,
			}
		} else {
			nextStage := stages[i+1]
			cursorTarget = &types.CursorPredictionTarget{
				RelativePath:    filePath,
				LineNumber:      int32(nextStage.BufferStart),
				ShouldRetrigger: false,
			}
		}

		// Populate the stage's exported fields
		stage.Lines = stageLines
		stage.Changes = remappedChanges
		stage.Groups = groups
		stage.CursorLine = targetCursorLine
		stage.CursorCol = targetCursorCol
		stage.CursorTarget = cursorTarget
		stage.IsLastStage = isLastStage

		// Clear rawChanges (no longer needed)
		stage.rawChanges = nil
	}
}

// JoinLines joins a slice of strings with newlines.
// Each line gets a trailing \n, which is the standard line terminator format
// that diffmatchpatch expects. This ensures proper line counting:
// - ["a", "b"] → "a\nb\n" (2 lines)
// - ["a", ""] → "a\n\n" (2 lines, second is empty)
func JoinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
