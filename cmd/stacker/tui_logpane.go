package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type logPaneState struct {
	TopLine       int
	TopWrappedRow int
	Follow        bool
	ExplicitPause bool
	Wrap          bool
	Evicted       bool
	Selecting     bool
	SelStart      int
	SelEnd        int
}

type paneVisualLine struct {
	text       string
	line       int
	wrappedRow int
}

func paneContentWidth(rect paneRect) int {
	return max(10, rect.Width-5)
}

func paneVisibleLines(rect paneRect) int {
	return max(1, rect.Height-4)
}

func paneVisualLines(p *Process, rect paneRect, wrap bool) ([]paneVisualLine, int, int) {
	start, logs, next := p.TailLogs(0)
	width := paneContentWidth(rect)
	visual := make([]paneVisualLine, 0, len(logs))
	for i, line := range logs {
		if !wrap || ansi.StringWidth(line) <= width {
			visual = append(visual, paneVisualLine{text: truncate(line, width), line: start + i})
			continue
		}
		for row, part := range strings.Split(ansi.Hardwrap(line, width, true), "\n") {
			visual = append(visual, paneVisualLine{text: part, line: start + i, wrappedRow: row})
		}
	}
	return visual, start, next
}

func paneTopIndex(visual []paneVisualLine, state *logPaneState, start, visible int) int {
	if len(visual) == 0 {
		return 0
	}
	maximum := max(0, len(visual)-visible)
	if state.Follow {
		return maximum
	}
	if state.TopLine < start {
		state.TopLine = start
		state.TopWrappedRow = 0
		state.Evicted = true
	}
	for index, line := range visual {
		if line.line > state.TopLine || line.line == state.TopLine && line.wrappedRow >= state.TopWrappedRow {
			return min(index, maximum)
		}
	}
	return maximum
}

func paneSetTop(state *logPaneState, visual []paneVisualLine, index int) {
	if len(visual) == 0 {
		state.TopLine = 0
		state.TopWrappedRow = 0
		return
	}
	line := visual[clamp(index, 0, len(visual)-1)]
	state.TopLine = line.line
	state.TopWrappedRow = line.wrappedRow
}

func renderLogPane(p *Process, state *logPaneState, rect paneRect, focused bool) string {
	visual, start, next := paneVisualLines(p, rect, state.Wrap)
	visible := paneVisibleLines(rect)
	top := paneTopIndex(visual, state, start, visible)
	paneSetTop(state, visual, top)
	end := min(len(visual), top+visible)
	mode := "LIVE"
	if !state.Follow {
		mode = "PAUSED"
	}
	title := fmt.Sprintf("Logs: %s [%s] — %s", sanitizeLogLine(p.Name), p.Status(), mode)
	if state.Wrap {
		title += " — wrap"
	}
	if errs := p.Errors(); errs > 0 {
		title += fmt.Sprintf(" — %d error line(s)", errs)
	}
	if !state.Follow && end > 0 {
		if pending := next - visual[end-1].line - 1; pending > 0 {
			title += fmt.Sprintf(" — %d new", pending)
		}
	}
	if focused {
		title = "● " + title
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(truncate(title, paneContentWidth(rect))))
	for i := top; i < end; i++ {
		b.WriteByte('\n')
		line := visual[i].text
		if state.SelStart >= 0 && state.SelEnd >= 0 && visual[i].line >= min(state.SelStart, state.SelEnd) && visual[i].line <= max(state.SelStart, state.SelEnd) {
			line = selectionStyle.Render(line)
		}
		b.WriteString(line)
	}
	return b.String()
}

func scrollLogPane(p *Process, state *logPaneState, rect paneRect, delta int) {
	visual, start, _ := paneVisualLines(p, rect, state.Wrap)
	visible := paneVisibleLines(rect)
	top := paneTopIndex(visual, state, start, visible)
	maximum := max(0, len(visual)-visible)
	top = clamp(top+delta, 0, maximum)
	paneSetTop(state, visual, top)
	state.Follow = top == maximum && !state.ExplicitPause
}

func followLogPane(p *Process, state *logPaneState, rect paneRect) {
	visual, _, _ := paneVisualLines(p, rect, state.Wrap)
	paneSetTop(state, visual, max(0, len(visual)-paneVisibleLines(rect)))
	state.Follow = true
	state.ExplicitPause = false
}

func paneMouseLine(p *Process, state *logPaneState, rect paneRect, y int) int {
	visual, start, _ := paneVisualLines(p, rect, state.Wrap)
	if len(visual) == 0 {
		return start
	}
	top := paneTopIndex(visual, state, start, paneVisibleLines(rect))
	row := clamp(top+y-rect.Y-2, 0, len(visual)-1)
	return visual[row].line
}

func selectedPaneText(p *Process, state *logPaneState) (string, int) {
	if state.SelStart < 0 || state.SelEnd < 0 {
		return "", 0
	}
	start, logs, next := p.TailLogs(0)
	from := max(start, min(state.SelStart, state.SelEnd))
	to := min(next-1, max(state.SelStart, state.SelEnd))
	if len(logs) == 0 || from > to {
		return "", 0
	}
	return strings.Join(logs[from-start:to-start+1], "\n"), to - from + 1
}
