package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestReject(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{StartLine: 1, EndLineInc: 1, Lines: []string{"test"}}}
	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 5}

	eng.reject()

	assert.Equal(t, stateIdle, eng.state, "state after reject")
	assert.Nil(t, eng.completions, "completions after reject")
	assert.Nil(t, eng.cursorTarget, "cursorTarget after reject")
	assert.Greater(t, buf.clearUICalls, 0, "ClearUI should have been called")
}

func TestClearState_Options(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.completions = []*types.Completion{{StartLine: 1, EndLineInc: 1, Lines: []string{"test"}}}
	eng.stagedCompletion = &text.StagedCompletion{CurrentIdx: 0}
	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 5}

	eng.clearState(ClearOptions{
		ClearStaged:       false,
		ClearCursorTarget: true,
		CallOnReject:      true,
	})

	if eng.stagedCompletion == nil {
		assert.NotNil(t, eng.stagedCompletion, "stagedCompletion should be preserved when ClearStaged=false")
	}
	assert.Nil(t, eng.cursorTarget, "cursorTarget should be cleared when ClearCursorTarget=true")
	assert.Nil(t, eng.completions, "completions should always be cleared")
}

func TestPartialAccept_AppendChars_SingleWord(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"func"}
	buf.row = 1
	buf.col = 4
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"function foo()"},
	}}
	eng.completionOriginalLines = []string{"func"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   4,
		Lines:      []string{"function foo()"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, "tion ", buf.lastInsertedText, "inserted text")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial accept")
}

func TestPartialAccept_AppendChars_Punctuation(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"foo"}
	buf.row = 1
	buf.col = 3
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"foo.bar.baz"},
	}}
	eng.completionOriginalLines = []string{"foo"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   3,
		Lines:      []string{"foo.bar.baz"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, ".", buf.lastInsertedText, "inserted text at punctuation")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial accept")
}

func TestPartialAccept_AppendChars_NoRemaining(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello!"},
	}}
	eng.completionOriginalLines = []string{"hello"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   5,
		Lines:      []string{"hello!"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, "!", buf.lastInsertedText, "inserted text")
	assert.Equal(t, stateIdle, eng.state, "state when nothing remaining")
}

func TestPartialAccept_MultiLine_FirstLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2", "line 3"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 3,
		Lines:      []string{"new line 1", "new line 2", "new line 3"},
	}}
	eng.completionOriginalLines = []string{"line 1", "line 2", "line 3"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		Lines:      []string{"new line 1", "new line 2", "new line 3"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, 1, buf.lastReplacedLine, "replaced line number")
	assert.Equal(t, "new line 1", buf.lastReplacedContent, "replaced content")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial line accept")
	assert.Equal(t, 2, len(eng.completions[0].Lines), "remaining lines")
	assert.Equal(t, 2, eng.completions[0].StartLine, "updated start line")
	assert.Equal(t, 3, eng.completions[0].EndLineInc, "end line unchanged for equal line count")
}

func TestPartialAccept_MultiLine_LastLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"old line"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"new line"},
	}}
	eng.completionOriginalLines = []string{"old line"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		Lines:      []string{"new line"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, "new line", buf.lastReplacedContent, "replaced content")
	assert.Equal(t, stateIdle, eng.state, "state after accepting last line")
}

func TestPartialAccept_WithUserTyping(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"functi"}
	buf.row = 1
	buf.col = 6
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"function foo()"},
	}}
	eng.completionOriginalLines = []string{"func"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   4,
		Lines:      []string{"function foo()"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, "on ", buf.lastInsertedText, "inserted text after user typing")
}

func TestPartialAccept_NoCompletions(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = nil

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, stateHasCompletion, eng.state, "state unchanged when no completions")
}

func TestPartialAccept_NoGroups(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"test"},
	}}
	eng.currentGroups = nil

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, stateHasCompletion, eng.state, "state unchanged when no groups")
}

func TestPartialAccept_AdditionGroup(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"func main() {", "}"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 2,
		Lines:      []string{"func main() {", "    fmt.Println(\"hello\")", "}"},
	}}
	eng.completionOriginalLines = []string{"func main() {", "}"}
	eng.currentGroups = []*text.Group{{
		Type:       "addition",
		BufferLine: 2,
		StartLine:  2,
		EndLine:    2,
		Lines:      []string{"    fmt.Println(\"hello\")"},
	}}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.Equal(t, 1, buf.lastReplacedLine, "replaced line number")
	assert.Equal(t, "func main() {", buf.lastReplacedContent, "replaced content")
	assert.Equal(t, stateHasCompletion, eng.state, "state after first partial")
	assert.Equal(t, 2, len(eng.completions[0].Lines), "remaining lines")
	assert.Equal(t, 2, eng.completions[0].StartLine, "updated start line")
	assert.Equal(t, 3, eng.completions[0].EndLineInc, "updated end line for addition")
}

// TestPartialAccept_AppendCharsWithAddition tests that when a multi-line stage
// has an append_chars line followed by addition lines, completing the append_chars
// line transitions to the addition lines (not skipping them).
func TestPartialAccept_AppendCharsWithAddition(t *testing.T) {
	buf := newMockBuffer()
	// Buffer: line 3 is "def bubble_sort(arr):" (already complete after partial accepts)
	buf.lines = []string{"import numpy as np", "", "def bubble_sort(arr):"}
	buf.row = 3
	buf.col = 21 // At end of line
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Completion for stage 1: line 3 (append_chars) + line 4 (addition)
	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  3,
		EndLineInc: 3, // Only replacing line 3, but adding line 4
		Lines:      []string{"def bubble_sort(arr):", "    n = len(arr)"},
	}}
	eng.completionOriginalLines = []string{"def bubble_sort(arr):"}

	// Groups: first is append_chars (complete), second is addition
	eng.currentGroups = []*text.Group{
		{
			Type:       "modification",
			BufferLine: 3,
			StartLine:  1,
			EndLine:    1,
			Lines:      []string{"def bubble_sort(arr):"},
			OldLines:   []string{"def bubble_sort(arr):"}, // Same as current - already complete
			RenderHint: "append_chars",
			ColStart:   21, // Already at end
			ColEnd:     21,
		},
		{
			Type:       "addition",
			BufferLine: 4,
			StartLine:  2,
			EndLine:    2,
			Lines:      []string{"    n = len(arr)"},
		},
	}

	// When append_chars line is already complete, partial accept should
	// transition to the next line (the addition), NOT finalize the stage
	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	// After partial accept, the completion should now point to the addition line
	assert.Equal(t, stateHasCompletion, eng.state, "should still be in HasCompletion")
	assert.Equal(t, 1, len(eng.completions[0].Lines), "should have 1 remaining line")
	assert.Equal(t, "    n = len(arr)", eng.completions[0].Lines[0], "remaining line content")
	assert.Equal(t, 4, eng.completions[0].StartLine, "startLine should be 4")
}

// TestPartialAccept_StagedCompletion_UsesCurrentGroups tests that during partial
// accept with a staged completion, we use currentGroups (updated by rerenderPartial)
// not the stale stage groups. This prevents skipping addition lines when the
// append_chars group in the stage is stale but currentGroups has been updated.
func TestPartialAccept_StagedCompletion_UsesCurrentGroups(t *testing.T) {
	buf := newMockBuffer()
	// Buffer state after completing append_chars on line 3
	buf.lines = []string{"import numpy as np", "", "def bubble_sort(arr):"}
	buf.row = 3
	buf.col = 21
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Staged completion exists with OLD groups (before rerenderPartial updated them)
	eng.stagedCompletion = &text.StagedCompletion{
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 3,
				BufferEnd:   3,
				Lines:       []string{"def bubble_sort(arr):", "    n = len(arr)"},
				// These groups are STALE - first group is append_chars for line 3
				Groups: []*text.Group{
					{
						Type:       "modification",
						BufferLine: 3,
						StartLine:  1,
						EndLine:    1,
						Lines:      []string{"def bubble_sort(arr):"},
						OldLines:   []string{"def bubb"},
						RenderHint: "append_chars",
						ColStart:   8,
						ColEnd:     21,
					},
					{
						Type:       "addition",
						BufferLine: 4,
						StartLine:  2,
						EndLine:    2,
						Lines:      []string{"    n = len(arr)"},
					},
				},
			},
			// Next stage
			&text.Stage{
				BufferStart: 5,
				BufferEnd:   5,
				Lines:       []string{"    for i in range(n):"},
				Groups: []*text.Group{{
					Type:       "addition",
					BufferLine: 5,
					Lines:      []string{"    for i in range(n):"},
				}},
			},
		},
		CurrentIdx: 0,
	}

	// Current state: append_chars is complete, now showing addition for line 4
	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  4,
		EndLineInc: 4,
		Lines:      []string{"    n = len(arr)"},
	}}
	eng.completionOriginalLines = []string{} // No original lines since this is an addition

	// currentGroups has been updated by rerenderPartial - just the addition group
	eng.currentGroups = []*text.Group{{
		Type:       "addition",
		BufferLine: 4,
		StartLine:  1,
		EndLine:    1,
		Lines:      []string{"    n = len(arr)"},
	}}

	// This is the key: partial accept should use currentGroups (addition),
	// NOT the staged completion's groups (which have stale append_chars first)
	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	// Verify the addition line was inserted
	assert.Equal(t, 4, len(buf.lines), "buffer should have 4 lines after insert")
	assert.Equal(t, "    n = len(arr)", buf.lines[3], "line 4 should be the addition")
}

func TestPartialAccept_FinishSyncsBuffer_NonStaged(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"test"}
	buf.row = 1
	buf.col = 4
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"test!"},
	}}
	eng.completionOriginalLines = []string{"test"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   4,
		Lines:      []string{"test!"},
	}}
	eng.stagedCompletion = nil
	eng.cursorTarget = nil

	initialSyncCalls := buf.syncCalls

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.True(t, buf.syncCalls > initialSyncCalls, "buffer should be synced after finish")
	assert.Equal(t, stateIdle, eng.state, "should be idle after finish")
}

// TestPartialAccept_MultiLineCompletion_CursorTargetConsistency tests that cursor targets
// remain consistent when using partial accept vs full accept on the same multi-line completion.
func TestPartialAccept_MultiLineCompletion_CursorTargetConsistency(t *testing.T) {
	t.Run("full_accept_preserves_cursor_target", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"old line 1", "old line 2", "old line 3", "old line 4"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 4,
			Lines:      []string{"new line 1", "new line 2", "new line 3", "new line 4"},
		}}
		eng.completionOriginalLines = buf.lines
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1},
			{Type: "modification", BufferLine: 2},
			{Type: "modification", BufferLine: 3},
			{Type: "modification", BufferLine: 4},
		}

		expectedCursorTarget := int32(8)
		eng.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    "test.go",
			LineNumber:      expectedCursorTarget,
			ShouldRetrigger: true,
		}
		eng.applyBatch = &mockBatch{}
		eng.stagedCompletion = nil

		eng.doAcceptCompletion(Event{Type: EventAccept})

		assert.Equal(t, int(expectedCursorTarget), buf.showCursorTargetLine, "cursor target should be preserved after full accept")
	})

	t.Run("partial_accept_4_lines_one_by_one_same_target", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"old line 1", "old line 2", "old line 3", "old line 4"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 4,
			Lines:      []string{"new line 1", "new line 2", "new line 3", "new line 4"},
		}}
		eng.completionOriginalLines = buf.lines
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1},
			{Type: "modification", BufferLine: 2},
			{Type: "modification", BufferLine: 3},
			{Type: "modification", BufferLine: 4},
		}

		expectedCursorTarget := int32(8)
		eng.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    "test.go",
			LineNumber:      expectedCursorTarget,
			ShouldRetrigger: true,
		}
		eng.stagedCompletion = nil

		eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})
		assert.Equal(t, stateHasCompletion, eng.state, "should stay in HasCompletion after partial accept")
		assert.Equal(t, 3, len(eng.completions[0].Lines), "remaining lines")
		assert.Equal(t, 2, eng.completions[0].StartLine, "start line increments")

		eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})
		assert.Equal(t, 2, len(eng.completions[0].Lines), "remaining lines")
		assert.Equal(t, 3, eng.completions[0].StartLine, "start line increments")

		eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})
		assert.Equal(t, 1, len(eng.completions[0].Lines), "remaining lines")
		assert.Equal(t, 4, eng.completions[0].StartLine, "start line increments")

		eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

		assert.Equal(t, int(expectedCursorTarget), buf.showCursorTargetLine, "cursor target should be preserved through partial accepts")
	})

	t.Run("partial_accept_cursor_target_consistency_through_all_accepts", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"x", "y", "z", "w"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		cursorTarget := int32(12)
		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 4,
			Lines:      []string{"X", "Y", "Z", "W"},
		}}
		eng.completionOriginalLines = buf.lines
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1},
			{Type: "modification", BufferLine: 2},
			{Type: "modification", BufferLine: 3},
			{Type: "modification", BufferLine: 4},
		}
		eng.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    "test.go",
			LineNumber:      cursorTarget,
			ShouldRetrigger: false,
		}
		eng.stagedCompletion = nil

		for i := 0; i < 3; i++ {
			eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})
			if i < 2 {
				assert.Equal(t, cursorTarget, eng.cursorTarget.LineNumber, "cursor target should be unchanged")
			}
		}

		eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

		assert.Equal(t, int(cursorTarget), buf.showCursorTargetLine, "final cursor target should be original value")
	})

	t.Run("partial_accept_with_staged_completion", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"a", "b", "c", "d", "e", "f"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		stage1 := &text.Stage{
			BufferStart: 1,
			BufferEnd:   2,
			Lines:       []string{"A", "B"},
			Groups:      []*text.Group{{Type: "modification", BufferLine: 1}},
			CursorLine:  1,
			CursorCol:   0,
			IsLastStage: false,
			CursorTarget: &types.CursorPredictionTarget{
				LineNumber:      3,
				ShouldRetrigger: false,
			},
		}

		stage2 := &text.Stage{
			BufferStart: 3,
			BufferEnd:   4,
			Lines:       []string{"C", "D"},
			Groups:      []*text.Group{{Type: "modification", BufferLine: 3}},
			CursorLine:  1,
			CursorCol:   0,
			IsLastStage: true,
			CursorTarget: &types.CursorPredictionTarget{
				LineNumber:      5,
				ShouldRetrigger: true,
			},
		}

		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 2,
			Lines:      []string{"A", "B"},
		}}
		eng.completionOriginalLines = []string{"a", "b"}
		eng.currentGroups = []*text.Group{{Type: "modification", BufferLine: 1}}
		eng.stagedCompletion = &text.StagedCompletion{
			Stages:     []*text.Stage{stage1, stage2},
			CurrentIdx: 0,
		}
		eng.applyBatch = &mockBatch{}
		eng.cursorTarget = stage1.CursorTarget

		eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

		assert.Equal(t, int32(3), eng.cursorTarget.LineNumber, "cursor target should be preserved from stage 1")
	})
}

// TestAdvanceStagedCompletion_AdditionGroupsSpanningMultipleOldLines verifies
// that cumulative offset is computed correctly when a stage has only addition
// groups but spans multiple old lines (BufferStart != BufferEnd). The stage
// replaces old lines, so oldLineCount must reflect the replaced range.
func TestAdvanceStagedCompletion_AdditionGroupsSpanningMultipleOldLines(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Stage 1: replaces old lines 6-7 with 5 new lines, all groups are additions
	stage1 := &text.Stage{
		BufferStart: 6,
		BufferEnd:   7,
		Lines:       []string{"", "", "def min_max(data):", "    return min(data)", ""},
		Groups: []*text.Group{
			{Type: "addition", StartLine: 1, EndLine: 2, BufferLine: 6},
			{Type: "addition", StartLine: 5, EndLine: 5, BufferLine: 8},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 13, RelativePath: "test.py"},
	}
	// Stage 2: further down, should be offset by stage 1's line count change
	stage2 := &text.Stage{
		BufferStart: 10,
		BufferEnd:   10,
		Lines:       []string{"", ""},
		Groups: []*text.Group{
			{Type: "addition", StartLine: 1, EndLine: 2, BufferLine: 10},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 15, RelativePath: "test.py"},
	}

	eng.stagedCompletion = &text.StagedCompletion{
		Stages:     []*text.Stage{stage1, stage2},
		CurrentIdx: 0,
	}

	eng.advanceStagedCompletion()

	// Stage 1 replaced 2 old lines with 5 new lines → offset = 5 - 2 = 3
	// CumulativeOffset is applied to remaining stages then reset to 0
	assert.Equal(t, 0, eng.stagedCompletion.CumulativeOffset, "offset reset after applying")
	// Stage 2 should be shifted by 3 (from 10 to 13)
	assert.Equal(t, 13, stage2.BufferStart, "stage 2 BufferStart shifted by 3")
	assert.Equal(t, 13, stage2.BufferEnd, "stage 2 BufferEnd shifted by 3")
}
