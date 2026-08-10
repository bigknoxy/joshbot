package main

// Interactive line editor for `joshbot agent` TTY mode.
//
// The editor is deliberately lightweight: raw mode is entered once around the
// whole input loop (owned by runAgentLoop), and this file only implements the
// key parsing, the edit buffer, history, slash-command completion and the
// on-screen redraw. It knows nothing about terminals — runAgentLoop supplies a
// keyReader (from the real stdin, or a test double), the terminal width and
// height, and the output writer.
//
// Controls:
//
//	Enter                   submit the line
//	Tab                     complete / cycle slash-command completions
//	Up / Down               history (single-line) or cursor lines (multiline)
//	Left / Right            move the cursor
//	Home / End              start / end of the current line
//	Backspace / Delete      delete the previous / next character
//	Alt+Enter (or Ctrl+J)   insert a newline (multiline editing)
//	Ctrl+C                  quit (returns io.EOF)
//	Ctrl+D                  quit on an empty buffer, otherwise delete forward
//
// The model-status prompt is supplied by the caller through promptFn. Colors
// use a warm amber/teal palette, deliberately distinct from Hermes's
// cyan/purple.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// keyCode enumerates the non-printable keys the editor understands.
type keyCode int

const (
	keyNone keyCode = iota
	keyEnter
	keyTab
	keyNewline
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyCtrlC
	keyCtrlD
)

// keyEvent is one parsed terminal input event. r is the printable character,
// or 0 when the event is a control key identified by code.
type keyEvent struct {
	r    rune
	code keyCode
}

// keyReader yields one key event per call. The editor never distinguishes a
// real terminal from a test double — osKeyReader is the production source and
// chanKeyReader exists for tests.
type keyReader interface {
	ReadKey() (keyEvent, error)
}

// lineEditor is the interactive input editor.
type lineEditor struct {
	out io.Writer

	reader   keyReader
	width    int
	height   int
	prompt   string // prompt text for the current ReadLine (already styled)
	promptFn func() string
	commands []string

	buf    []rune
	cursor int

	history []string
	histIdx int // == len(history) means editing a fresh line

	compMatches []string
	compIdx     int

	rows      int // terminal rows the current view occupies
	cursorRow int // row offset of the edit cursor within the view
	rendered  bool

	// One reader goroutine lives for the whole editor, forwarding keys to
	// keyCh. A per-ReadLine goroutine would outlive its ReadLine (blocked on
	// the next terminal read) and steal the first bytes of the following line.
	keyCh     chan keyEvent
	errCh     chan error
	stopCh    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

// editorStyles is the amber/teal palette for the TUI. Kept in one place so the
// look can be tuned without hunting through the file.
var editorStyles = struct {
	prompt lipgloss.Style // the "> " arrow
	badge  lipgloss.Style // the model chip in the prompt
	hint   lipgloss.Style // completion hints above the prompt
}{
	prompt: lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true), // amber
	badge:  lipgloss.NewStyle().Foreground(lipgloss.Color("43")),             // teal
	hint:   lipgloss.NewStyle().Foreground(lipgloss.Color("243")),            // muted grey
}

// newLineEditor builds an editor. promptFn is evaluated once per ReadLine so a
// /model switch shows up in the prompt of the next submitted line without a
// per-keypress session load. It may be nil.
func newLineEditor(out io.Writer, promptFn func() string, commands []string) *lineEditor {
	return &lineEditor{
		out:      out,
		width:    80,
		height:   24,
		promptFn: promptFn,
		commands: commands,
		histIdx:  0,
		keyCh:    make(chan keyEvent),
		errCh:    make(chan error, 1),
		stopCh:   make(chan struct{}),
	}
}

// setPromptFn replaces the prompt supplier.
func (e *lineEditor) setPromptFn(fn func() string) {
	e.promptFn = fn
}

// startReader launches the single reader goroutine on first use. It is safe to
// call repeatedly.
func (e *lineEditor) startReader() {
	e.startOnce.Do(func() {
		go e.readLoop()
	})
}

// readLoop forwards keys from the keyReader to keyCh until the reader fails or
// the editor is closed.
func (e *lineEditor) readLoop() {
	for {
		ev, err := e.reader.ReadKey()
		if err != nil {
			select {
			case e.errCh <- err:
			case <-e.stopCh:
			}
			return
		}
		select {
		case e.keyCh <- ev:
		case <-e.stopCh:
			return
		}
	}
}

// close stops the reader goroutine. Safe to call more than once. The goroutine
// may still be blocked inside ReadKey; like the buffered read in runAgentLoop,
// it exits once a byte arrives or the descriptor closes.
func (e *lineEditor) close() {
	e.closeOnce.Do(func() { close(e.stopCh) })
}

// ReadLine blocks until the user submits a line (or the context ends), then
// returns the line. Ctrl+C and Ctrl+D on an empty buffer return io.EOF so the
// caller treats them as "quit". If ctx is cancelled while blocked, the editor
// clears its view and returns ctx.Err().
func (e *lineEditor) ReadLine(ctx context.Context) (string, error) {
	e.startReader()
	e.reset()
	if e.promptFn != nil {
		e.prompt = e.promptFn()
	}

	e.render()
	for {
		select {
		case <-ctx.Done():
			e.hide()
			return "", ctx.Err()
		case err := <-e.errCh:
			e.hide()
			return "", err
		case ev := <-e.keyCh:
			done, line, err := e.handleKey(ev)
			if err != nil {
				e.hide()
				return "", err
			}
			if done {
				e.hide()
				return line, nil
			}
			e.render()
		}
	}
}

// reset clears the per-line edit state (but keeps history).
func (e *lineEditor) reset() {
	e.buf = nil
	e.cursor = 0
	e.histIdx = len(e.history)
	e.compMatches = nil
	e.compIdx = 0
	e.rows = 0
	e.cursorRow = 0
	e.rendered = false
}

// Current returns the current buffer contents (used by tests).
func (e *lineEditor) Current() string {
	return string(e.buf)
}

// handleKey applies one key event. done is true when the line is submitted.
func (e *lineEditor) handleKey(ev keyEvent) (done bool, line string, err error) {
	switch ev.code {
	case keyTab:
		e.complete()
		return false, "", nil
	case keyEnter:
		e.pushHistory(string(e.buf))
		return true, string(e.buf), nil
	case keyCtrlC:
		return false, "", io.EOF
	case keyCtrlD:
		if len(e.buf) == 0 {
			return false, "", io.EOF
		}
		e.deleteForward()
		e.compReset()
		return false, "", nil
	case keyNewline:
		e.insert('\n')
		e.compReset()
		return false, "", nil
	case keyBackspace:
		e.backspace()
		e.compReset()
		return false, "", nil
	case keyDelete:
		e.deleteForward()
		e.compReset()
		return false, "", nil
	case keyLeft:
		e.moveLeft()
		e.compReset()
		return false, "", nil
	case keyRight:
		e.moveRight()
		e.compReset()
		return false, "", nil
	case keyUp:
		e.moveUp()
		e.compReset()
		return false, "", nil
	case keyDown:
		e.moveDown()
		e.compReset()
		return false, "", nil
	case keyHome:
		e.toLineStart()
		e.compReset()
		return false, "", nil
	case keyEnd:
		e.toLineEnd()
		e.compReset()
		return false, "", nil
	case keyNone:
		if ev.r != 0 {
			e.insert(ev.r)
			e.compReset()
		}
		return false, "", nil
	default:
		return false, "", nil
	}
}

// pushHistory records a submitted non-empty line for Up/Down recall.
func (e *lineEditor) pushHistory(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == line {
		return
	}
	e.history = append(e.history, line)
	e.histIdx = len(e.history)
}

func (e *lineEditor) insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.cursor+1:], e.buf[e.cursor:])
	e.buf[e.cursor] = r
	e.cursor++
}

func (e *lineEditor) backspace() {
	if e.cursor <= 0 {
		return
	}
	e.buf = append(e.buf[:e.cursor-1], e.buf[e.cursor:]...)
	e.cursor--
}

func (e *lineEditor) deleteForward() {
	if e.cursor >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.cursor], e.buf[e.cursor+1:]...)
}

func (e *lineEditor) moveLeft() {
	if e.cursor > 0 {
		e.cursor--
	}
}

func (e *lineEditor) moveRight() {
	if e.cursor < len(e.buf) {
		e.cursor++
	}
}

// lineBounds returns the rune indexes of the start and end of the line holding
// cursor.
func (e *lineEditor) lineBounds(cursor int) (int, int) {
	start := 0
	for i := cursor - 1; i >= 0; i-- {
		if e.buf[i] == '\n' {
			start = i + 1
			break
		}
	}
	end := len(e.buf)
	for i := cursor; i < len(e.buf); i++ {
		if e.buf[i] == '\n' {
			end = i
			break
		}
	}
	return start, end
}

// lineOf returns the zero-based logical line number containing cursor.
func (e *lineEditor) lineOf(cursor int) int {
	n := 0
	for i := 0; i < cursor; i++ {
		if e.buf[i] == '\n' {
			n++
		}
	}
	return n
}

func (e *lineEditor) toLineStart() {
	start, _ := e.lineBounds(e.cursor)
	e.cursor = start
}

func (e *lineEditor) toLineEnd() {
	_, end := e.lineBounds(e.cursor)
	e.cursor = end
}

// moveUp in a multiline buffer moves to the previous line at the same column;
// in a single-line buffer it recalls earlier history.
func (e *lineEditor) moveUp() {
	if !strings.ContainsRune(string(e.buf), '\n') {
		e.historyUp()
		return
	}
	start, _ := e.lineBounds(e.cursor)
	line := e.lineOf(e.cursor)
	if line == 0 {
		return
	}
	col := e.cursor - start
	prevStart := e.lineStart(line - 1)
	prevEnd := prevStart
	for prevEnd < len(e.buf) && e.buf[prevEnd] != '\n' {
		prevEnd++
	}
	e.cursor = prevStart + min(col, prevEnd-prevStart)
}

// moveDown in a multiline buffer moves to the next line at the same column; in
// a single-line buffer it recalls more recent history.
func (e *lineEditor) moveDown() {
	if !strings.ContainsRune(string(e.buf), '\n') {
		e.historyDown()
		return
	}
	start, end := e.lineBounds(e.cursor)
	if end == len(e.buf) {
		return
	}
	col := e.cursor - start
	nextStart := end + 1
	nextEnd := nextStart
	for nextEnd < len(e.buf) && e.buf[nextEnd] != '\n' {
		nextEnd++
	}
	e.cursor = nextStart + min(col, nextEnd-nextStart)
}

// lineStart returns the rune index just past the (n-1)th newline, i.e. the
// start of logical line n.
func (e *lineEditor) lineStart(n int) int {
	if n <= 0 {
		return 0
	}
	idx := 0
	for i := 0; i < n; i++ {
		for idx < len(e.buf) && e.buf[idx] != '\n' {
			idx++
		}
		idx++ // skip the newline
	}
	if idx > len(e.buf) {
		idx = len(e.buf)
	}
	return idx
}

func (e *lineEditor) historyUp() {
	if e.histIdx <= 0 {
		return
	}
	e.histIdx--
	e.setBuffer(e.history[e.histIdx])
}

func (e *lineEditor) historyDown() {
	if e.histIdx >= len(e.history) {
		return
	}
	e.histIdx++
	if e.histIdx == len(e.history) {
		e.setBuffer("")
		return
	}
	e.setBuffer(e.history[e.histIdx])
}

func (e *lineEditor) setBuffer(s string) {
	e.buf = []rune(s)
	e.cursor = len(e.buf)
}

// complete handles Tab: it completes the slash command word currently being
// typed. The first Tab on a prefix with several matches starts cycling through
// them while a hint line lists the candidates.
func (e *lineEditor) complete() {
	if len(e.buf) == 0 || e.buf[0] != '/' {
		return
	}
	// The word after the slash, up to a space or the cursor, whichever first.
	start := 1
	end := start
	for end < len(e.buf) && e.buf[end] != ' ' && e.buf[end] != '\t' {
		end++
	}
	prefix := strings.ToLower(string(e.buf[start:end]))

	// Rebuild the candidate list when the prefix changed or the cycle ran out.
	if e.compMatches == nil || e.compIdx >= len(e.compMatches) || !strings.HasPrefix(e.compMatches[e.compIdx], prefix) {
		e.compMatches = nil
		for _, c := range e.commands {
			if strings.HasPrefix(strings.ToLower(c), prefix) {
				e.compMatches = append(e.compMatches, c)
			}
		}
		if len(e.compMatches) == 0 {
			e.compReset()
			return
		}
		e.compIdx = 0
	} else {
		e.compIdx = (e.compIdx + 1) % len(e.compMatches)
	}

	// Replace the command word (slash already present in buf[0], so do not
	// prepend another).
	replacement := []rune(e.compMatches[e.compIdx])
	rest := append([]rune(nil), e.buf[end:]...)
	e.buf = append(append(e.buf[:start], replacement...), rest...)
	e.cursor = start + len(replacement)
}

func (e *lineEditor) compReset() {
	e.compMatches = nil
	e.compIdx = 0
}

// editorView is the fully computed on-screen picture.
type editorView struct {
	rows      []string // rows to print, prompt already prefixed on row 0
	cursorRow int      // row offset of the edit cursor within rows
	cursorCol int      // column offset of the edit cursor
}

// buildView wraps the buffer into terminal rows (soft wrap), prefixes the
// prompt on row 0, renders completion hints above, and computes the edit
// cursor's position. Row 0 shares its budget with the prompt; every other row
// gets the full width.
func (e *lineEditor) buildView() editorView {
	prompt := e.prompt
	promptW := 0
	if prompt != "" {
		promptW = lipgloss.Width(prompt)
	}
	width := e.width
	if width <= 0 {
		width = 80
	}
	firstBudget := width - promptW
	if firstBudget < 4 {
		firstBudget = 4
	}

	lines := strings.Split(string(e.buf), "\n")

	// Locate the cursor's logical line and offset within it.
	cursorLine, cursorOff := 0, 0
	remaining := e.cursor
	for i, l := range lines {
		if remaining <= len(l) {
			cursorLine, cursorOff = i, remaining
			break
		}
		remaining -= len(l) + 1
	}

	// Wrap each logical line and record per-line chunk boundaries.
	type lineChunks struct {
		chunks    [][]rune
		lineStart int // index into rows where this line's chunks begin
	}
	chunked := make([]lineChunks, 0, len(lines))
	rowCount := 0
	for i, l := range lines {
		budget := firstBudget
		if i > 0 {
			budget = width
		}
		chunks := wrapRunes([]rune(l), budget, width)
		chunked = append(chunked, lineChunks{chunks: chunks, lineStart: rowCount})
		rowCount += len(chunks)
	}

	// Locate the cursor's chunk and offset within it, and compute its display
	// column. The column is a sum of rune widths, not the raw rune offset:
	// a wide rune (CJK, width 2) before the cursor would otherwise put the
	// cursor one cell left of its true position.
	cursorRow, cursorCol := 0, 0
	if cursorLine < len(chunked) {
		lc := chunked[cursorLine]
		remaining := cursorOff
		found := false
		for ri, chunk := range lc.chunks {
			if remaining <= len(chunk) {
				cursorRow = lc.lineStart + ri
				cursorCol = displayWidth(chunk[:remaining])
				if cursorRow == 0 {
					cursorCol += promptW
				}
				found = true
				break
			}
			remaining -= len(chunk)
		}
		if !found {
			cursorRow = lc.lineStart + len(lc.chunks) - 1
			last := lc.chunks[len(lc.chunks)-1]
			cursorCol = displayWidth(last)
			if cursorRow == 0 {
				cursorCol += promptW
			}
		}
	}

	// Compose display rows.
	rows := make([]string, 0, rowCount)
	for i, lc := range chunked {
		for ri, chunk := range lc.chunks {
			row := string(chunk)
			if i == 0 && ri == 0 && prompt != "" {
				row = prompt + row
			}
			rows = append(rows, row)
		}
	}

	// Completion hint above the prompt.
	hint := ""
	if len(e.compMatches) > 0 {
		var parts []string
		for i, m := range e.compMatches {
			text := "/" + m
			if i == e.compIdx {
				text = editorStyles.hint.Bold(true).Render("/" + m)
			}
			parts = append(parts, editorStyles.hint.Render(text))
		}
		hint = editorStyles.hint.Render("complete: " + strings.Join(parts, " "))
	}
	if hint != "" {
		rows = append([]string{hint}, rows...)
		cursorRow++
	}

	// Keep the view inside the terminal: drop the earliest rows when the buffer
	// would scroll the top of the view off screen, never past the cursor row.
	height := e.height
	if height <= 0 {
		height = 24
	}
	if maxRows := height - 1; len(rows) > maxRows {
		drop := len(rows) - maxRows
		if drop > cursorRow {
			drop = cursorRow
		}
		rows = rows[drop:]
		cursorRow -= drop
	}

	return editorView{rows: rows, cursorRow: cursorRow, cursorCol: cursorCol}
}

// wrapRunes chunks a logical line into visual rows: the first is limited to
// firstBudget (which accounts for the prompt), the rest to rowBudget. Wide
// runes are not split across rows.
func wrapRunes(runes []rune, firstBudget, rowBudget int) [][]rune {
	if len(runes) == 0 {
		return [][]rune{{}}
	}
	var out [][]rune
	budget := firstBudget
	start := 0
	for start < len(runes) {
		end := start + budget
		if end > len(runes) {
			end = len(runes)
		}
		for end > start && runeWidth(runes[end-1]) > 1 {
			end--
		}
		if end == start {
			end = start + 1
		}
		out = append(out, runes[start:end])
		start = end
		budget = rowBudget
	}
	return out
}

// runeWidth reports the display width of a rune (wide CJK runes take 2 cells).
func runeWidth(r rune) int {
	if r >= 0x1100 && (r <= 0x115F ||
		r == 0x2329 || r == 0x232A ||
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE19) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6)) {
		return 2
	}
	return 1
}

// displayWidth returns the total display-column width of a rune slice.
func displayWidth(rs []rune) int {
	w := 0
	for _, r := range rs {
		w += runeWidth(r)
	}
	return w
}

// render redraws the prompt + buffer on screen and positions the edit cursor.
func (e *lineEditor) render() {
	view := e.buildView()

	e.hide()

	fmt.Fprint(e.out, "\r")
	for _, row := range view.rows {
		fmt.Fprint(e.out, row, "\r\n")
	}

	e.rows = len(view.rows)
	e.cursorRow = view.cursorRow
	e.rendered = true

	// After the trailing \r\n the terminal cursor is one line below the last
	// row; move up to the cursor row and then right to the cursor column.
	if up := e.rows - 1 - view.cursorRow; up > 0 {
		fmt.Fprintf(e.out, "\x1b[%dA", up)
	}
	if view.cursorCol > 0 {
		fmt.Fprintf(e.out, "\x1b[%dC", view.cursorCol)
	}
}

// hide clears the rendered view, leaving the terminal cursor at the top of
// where the view was so the next output starts on a clean line.
func (e *lineEditor) hide() {
	if !e.rendered {
		return
	}
	if e.cursorRow > 0 {
		fmt.Fprintf(e.out, "\r\x1b[%dA", e.cursorRow)
	} else {
		fmt.Fprint(e.out, "\r")
	}
	fmt.Fprint(e.out, "\x1b[J")
	e.rendered = false
	e.rows = 0
	e.cursorRow = 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildEditorPrompt renders the prompt for a ReadLine turn, showing the
// session's current model when one is known.
func buildEditorPrompt(model string) string {
	if model != "" {
		return editorStyles.badge.Render("● "+model+" ") + editorStyles.prompt.Render("❯ ")
	}
	return editorStyles.prompt.Render("❯ ")
}
