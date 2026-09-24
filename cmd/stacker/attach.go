package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// runAttach opens a TUI against a running serve instance. q detaches only.
func runAttach(configPath string) int {
	client, st, err := newControlClient(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if st.Mode != "" && st.Mode != "serve" {
		fmt.Fprintf(os.Stderr, "instance is in %q mode; attach is only for serve\n", st.Mode)
		fmt.Fprintln(os.Stderr, "session mode already has a TUI — use that terminal, or stacker down first")
		return 1
	}

	m := newAttachModel(client, st.Config)
	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "attach error:", err)
		return 1
	}
	return 0
}

type attachTickMsg struct{}
type attachSnapMsg struct {
	seq   uint64
	procs []ProcessInfo
	err   error
}
type attachLogsMsg struct {
	seq        uint64
	generation uint64
	name       string
	lines      []string
	next       int
	err        error
}
type attachStatusMsg string

// attachWebMsg carries the outcome of a /v1/web toggle. The URL is built on
// this side, from addr, because only the client knows which address its own
// browser can reach.
type attachWebMsg struct {
	addr    string
	enabled bool
	err     error
}

type attachModel struct {
	client           *controlClient
	configPath       string
	collapsedPath    string
	collapsedPathErr error
	collapsed        map[string]bool

	procs               []ProcessInfo
	selected            int
	hasSnapshot         bool
	snapshotSeq         uint64
	selectionGeneration uint64
	logRequestSeq       uint64
	logInFlightSeq      uint64
	logInFlight         bool
	logs                []string
	logNext             int
	logName             string

	width, height int
	logOffset     int
	follow        bool
	wrap          bool
	showHelp      bool

	statusText string
	pollErr    string
}

func newAttachModel(client *controlClient, configPath string) *attachModel {
	m := &attachModel{
		client:     client,
		configPath: configPath,
		collapsed:  make(map[string]bool),
		selected:   0,
		follow:     true,
		wrap:       false,
	}
	if statePath, err := uiStatePath(configPath); err == nil {
		m.collapsedPath = statePath
		m.collapsed = loadCollapsedOrEmpty(statePath)
	} else {
		m.collapsedPathErr = err
	}
	return m
}

func (m *attachModel) Init() tea.Cmd {
	return tea.Batch(m.pollSnap(), m.tick())
}

func (m *attachModel) tick() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return attachTickMsg{} })
}

func (m *attachModel) pollSnap() tea.Cmd {
	m.snapshotSeq++
	seq := m.snapshotSeq
	return func() tea.Msg {
		var resp struct {
			OK        bool          `json:"ok"`
			Error     string        `json:"error"`
			Processes []ProcessInfo `json:"processes"`
		}
		if err := m.client.get("/v1/processes", &resp); err != nil {
			return attachSnapMsg{seq: seq, err: err}
		}
		if !resp.OK && resp.Error != "" {
			return attachSnapMsg{seq: seq, err: fmt.Errorf("%s", resp.Error)}
		}
		return attachSnapMsg{seq: seq, procs: resp.Processes}
	}
}

func (m *attachModel) pollLogs() tea.Cmd {
	name := m.currentName()
	if name == "" || m.logInFlight {
		return nil
	}
	m.logRequestSeq++
	seq := m.logRequestSeq
	generation := m.selectionGeneration
	m.logInFlight = true
	m.logInFlightSeq = seq
	from := m.logNext
	if m.logName != name {
		from = 0
	}
	return func() tea.Msg {
		path := "/v1/processes/" + url.PathEscape(name) + "/logs?from=" + fmt.Sprintf("%d", from)
		var resp struct {
			OK    bool     `json:"ok"`
			Error string   `json:"error"`
			Lines []string `json:"lines"`
			Next  int      `json:"next"`
		}
		if err := m.client.get(path, &resp); err != nil {
			return attachLogsMsg{seq: seq, generation: generation, name: name, err: err}
		}
		if !resp.OK && resp.Error != "" {
			return attachLogsMsg{seq: seq, generation: generation, name: name, err: fmt.Errorf("%s", resp.Error)}
		}
		return attachLogsMsg{seq: seq, generation: generation, name: name, lines: resp.Lines, next: resp.Next}
	}
}

func (m *attachModel) currentName() string {
	rows := m.rows()
	if m.selected < 0 || m.selected >= len(rows) || rows[m.selected].Member == nil {
		return ""
	}
	index := rows[m.selected].Member.Index
	if index < 0 || index >= len(m.procs) {
		return ""
	}
	return m.procs[index].Name
}

func (m *attachModel) current() *ProcessInfo {
	rows := m.rows()
	if m.selected < 0 || m.selected >= len(rows) || rows[m.selected].Member == nil {
		return nil
	}
	index := rows[m.selected].Member.Index
	if index < 0 || index >= len(m.procs) {
		return nil
	}
	return &m.procs[index]
}

func (m *attachModel) members() []sectionMember {
	members := make([]sectionMember, 0, len(m.procs))
	for index, process := range m.procs {
		members = append(members, sectionMember{
			Name:     process.Name,
			Group:    process.Group,
			OneShot:  process.OneShot,
			Orphaned: process.Orphaned,
			Index:    index,
		})
	}
	return members
}

func (m *attachModel) sections() []section {
	return buildSections(m.members())
}

func (m *attachModel) rows() []listRow {
	return buildRows(m.sections(), m.collapsed)
}

func (m *attachModel) summarizeSection(current *section) sectionSummary {
	states := make([]memberState, 0, len(current.Services)+len(current.Tasks))
	for _, members := range [][]sectionMember{current.Services, current.Tasks} {
		for _, member := range members {
			if member.Index < 0 || member.Index >= len(m.procs) {
				continue
			}
			process := m.procs[member.Index]
			states = append(states, memberState{
				Status:  ProcessStatus(process.Status),
				Errors:  process.Errors,
				OneShot: member.OneShot,
			})
		}
	}
	return summarizeSection(states)
}

func (m *attachModel) selectedSection() *section {
	rows := m.rows()
	if m.selected < 0 || m.selected >= len(rows) {
		return nil
	}
	return rows[m.selected].Header
}

func (m *attachModel) selectedOrEnclosingSection() *section {
	rows := m.rows()
	if m.selected < 0 || m.selected >= len(rows) {
		return nil
	}
	if rows[m.selected].Header != nil {
		return rows[m.selected].Header
	}
	for index := m.selected; index >= 0; index-- {
		if rows[index].Header != nil {
			return rows[index].Header
		}
	}
	return nil
}

func (m *attachModel) selectedIdentity() (string, bool) {
	rows := m.rows()
	if m.selected < 0 || m.selected >= len(rows) {
		return "", false
	}
	row := rows[m.selected]
	if row.Header != nil {
		return row.Header.Name, true
	}
	if row.Member != nil {
		return row.Member.Name, false
	}
	return "", false
}

func (m *attachModel) restoreSelection(name string, header bool) {
	if name != "" {
		if header {
			if m.selectHeader(name) {
				return
			}
		} else if m.selectByName(name) || m.selectCollapsedSectionForMember(name) {
			return
		}
	}
	m.selectFirstService()
}

func (m *attachModel) selectByName(name string) bool {
	for index, row := range m.rows() {
		if row.Member != nil && row.Member.Name == name {
			m.selected = index
			return true
		}
	}
	return false
}

func (m *attachModel) selectHeader(name string) bool {
	for index, row := range m.rows() {
		if row.Header != nil && row.Header.Name == name {
			m.selected = index
			return true
		}
	}
	return false
}

func (m *attachModel) selectCollapsedSectionForMember(name string) bool {
	for _, current := range m.sections() {
		if !m.collapsed[current.Name] {
			continue
		}
		for _, members := range [][]sectionMember{current.Services, current.Tasks} {
			for _, member := range members {
				if member.Name == name {
					return m.selectHeader(current.Name)
				}
			}
		}
	}
	return false
}

func (m *attachModel) selectFirstService() {
	for _, current := range m.sections() {
		if len(current.Services) == 0 {
			continue
		}
		for _, member := range current.Services {
			if m.selectByName(member.Name) || m.selectCollapsedSectionForMember(member.Name) {
				return
			}
		}
	}
	rows := m.rows()
	if len(rows) == 0 {
		m.selected = -1
	} else {
		m.selected = 0
	}
}

func (m *attachModel) foldSelectedSection(collapsed bool) {
	current := m.selectedOrEnclosingSection()
	if current == nil {
		return
	}
	if m.collapsed == nil {
		m.collapsed = make(map[string]bool)
	}
	m.collapsed[current.Name] = collapsed
	m.persistCollapsedState()
	if collapsed {
		m.selectHeader(current.Name)
	}
	m.resetLogs()
}

func (m *attachModel) persistCollapsedState() error {
	if m.collapsedPathErr != nil {
		m.statusText = "Collapsed state save failed: " + m.collapsedPathErr.Error()
		return m.collapsedPathErr
	}
	if m.collapsedPath == "" {
		err := fmt.Errorf("collapsed state path is unavailable")
		m.statusText = "Collapsed state save failed: " + err.Error()
		return err
	}
	if err := saveCollapsed(m.collapsedPath, m.collapsed, m.sections()); err != nil {
		m.statusText = "Collapsed state save failed: " + err.Error()
		return err
	}
	return nil
}

func (m *attachModel) toggleSection(current *section) {
	if current == nil {
		return
	}
	if m.collapsed == nil {
		m.collapsed = make(map[string]bool)
	}
	m.collapsed[current.Name] = !m.collapsed[current.Name]
	m.persistCollapsedState()
}

func (m *attachModel) postAction(action string) tea.Cmd {
	name := m.currentName()
	if name == "" {
		return nil
	}
	return func() tea.Msg {
		path := "/v1/processes/" + url.PathEscape(name) + "/" + action
		var resp struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := m.client.post(path, nil, &resp); err != nil {
			if resp.Error != "" {
				return attachStatusMsg(resp.Error)
			}
			return attachStatusMsg(err.Error())
		}
		return attachStatusMsg(action + " sent")
	}
}

func (m *attachModel) groupActionCmd(group, action string) tea.Cmd {
	effectiveGroup := strings.TrimSpace(group)
	if effectiveGroup == "Other" {
		effectiveGroup = ""
	}
	return func() tea.Msg {
		var resp struct {
			OK       bool     `json:"ok"`
			Error    string   `json:"error"`
			Affected []string `json:"affected"`
		}
		if err := m.client.post("/v1/groups/"+action, map[string]string{"group": effectiveGroup}, &resp); err != nil {
			if resp.Error != "" {
				return attachStatusMsg(resp.Error)
			}
			return attachStatusMsg(err.Error())
		}
		return attachStatusMsg(fmt.Sprintf("%s sent to %d group members", action, len(resp.Affected)))
	}
}

// toggleWebCmd flips the supervisor's web log viewer through the control plane.
// A POST with no body means "toggle", so the client never has to track state
// that the supervisor already owns.
func (m *attachModel) toggleWebCmd() tea.Cmd {
	return func() tea.Msg {
		var resp struct {
			OK      bool   `json:"ok"`
			Error   string `json:"error"`
			Enabled bool   `json:"enabled"`
			Addr    string `json:"addr"`
		}
		if err := m.client.post("/v1/web", nil, &resp); err != nil {
			if resp.Error != "" {
				return attachWebMsg{err: fmt.Errorf("%s", resp.Error)}
			}
			return attachWebMsg{err: err}
		}
		return attachWebMsg{addr: resp.Addr, enabled: resp.Enabled}
	}
}

// copyWebURLCmd puts the URL on the clipboard (OSC 52 works over SSH), keeping
// the side effect out of Update so the toggle stays testable.
func (m *attachModel) copyWebURLCmd(target string) tea.Cmd {
	return func() tea.Msg {
		if err := copyToClipboard(target); err != nil {
			return attachStatusMsg("Web logs: " + target)
		}
		return attachStatusMsg("Web logs: " + target + " (copied)")
	}
}

func (m *attachModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case attachWebMsg:
		switch {
		case msg.err != nil:
			m.statusText = "Web logs failed: " + msg.err.Error()
		case !msg.enabled:
			m.statusText = "Web logs stopped"
		default:
			target := webPublicBaseURL(msg.addr) + "/"
			if name := m.currentName(); name != "" {
				target = webLogsURL(msg.addr, name)
			}
			m.statusText = "Web logs: " + target
			return m, m.copyWebURLCmd(target)
		}
	case attachTickMsg:
		return m, tea.Batch(m.pollSnap(), m.pollLogs(), m.tick())
	case attachSnapMsg:
		if msg.seq != m.snapshotSeq {
			return m, nil
		}
		if msg.err != nil {
			m.pollErr = msg.err.Error()
			return m, nil
		}
		m.pollErr = ""
		firstSnapshot := !m.hasSnapshot
		prev, prevHeader := "", false
		if !firstSnapshot {
			prev, prevHeader = m.selectedIdentity()
		}
		m.procs = msg.procs
		m.hasSnapshot = true
		if len(m.procs) == 0 {
			m.selected = -1
			m.resetLogs()
			return m, nil
		}
		if firstSnapshot || prev == "" {
			m.selectFirstService()
		} else {
			m.restoreSelection(prev, prevHeader)
		}
		current, currentHeader := m.selectedIdentity()
		if current != prev || currentHeader != prevHeader {
			m.resetLogs()
		}
	case attachLogsMsg:
		requestMatches := m.logInFlight && msg.seq == m.logInFlightSeq
		if requestMatches {
			m.logInFlight = false
		}
		if !requestMatches || msg.name != m.currentName() || msg.generation != m.selectionGeneration {
			return m, nil
		}
		if msg.err != nil {
			m.pollErr = msg.err.Error()
			return m, nil
		}
		if m.logName != msg.name {
			m.logs = nil
			m.logNext = 0
			m.logName = msg.name
			m.logOffset = 0
		}
		if len(msg.lines) > 0 {
			m.logs = append(m.logs, msg.lines...)
			// Cap local buffer.
			const maxLocal = 10000
			if len(m.logs) > maxLocal {
				m.logs = append([]string(nil), m.logs[len(m.logs)-maxLocal:]...)
			}
		}
		if msg.next > m.logNext {
			m.logNext = msg.next
		}
		if m.follow {
			m.logOffset = max(0, len(m.logs)-m.visibleLines())
		}
	case attachStatusMsg:
		m.statusText = string(msg)
	case tea.KeyMsg:
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				m.resetLogs()
			}
		case "down", "j":
			if m.selected+1 < len(m.rows()) {
				m.selected++
				m.resetLogs()
			}
		case "left", "h":
			m.foldSelectedSection(true)
		case "right", "l":
			m.foldSelectedSection(false)
		case "s":
			if current := m.selectedSection(); current != nil {
				return m, m.groupActionCmd(current.Name, "stop")
			}
			return m, m.postAction("stop")
		case "enter":
			if current := m.selectedSection(); current != nil {
				return m, m.groupActionCmd(current.Name, "start")
			}
			return m, m.postAction("start")
		case "r":
			if current := m.selectedSection(); current != nil {
				return m, m.groupActionCmd(current.Name, "restart")
			}
			return m, m.postAction("restart")
		case "f":
			return m, m.postAction("free-port")
		case " ":
			if current := m.selectedSection(); current != nil {
				return m, m.groupActionCmd(current.Name, "mark")
			}
			return m, m.postAction("mark")
		case "m":
			return m, func() tea.Msg {
				var resp struct {
					OK     bool   `json:"ok"`
					Error  string `json:"error"`
					Marked int    `json:"marked"`
				}
				if err := m.client.post("/v1/mark-all", nil, &resp); err != nil {
					return attachStatusMsg(err.Error())
				}
				return attachStatusMsg(fmt.Sprintf("Marked %d running", resp.Marked))
			}
		case "w":
			return m, m.toggleWebCmd()
		case "W":
			m.wrap = !m.wrap
		case "pgup":
			m.follow = false
			m.logOffset = max(0, m.logOffset-m.visibleLines())
		case "pgdown":
			m.logOffset = min(max(0, len(m.logs)-m.visibleLines()), m.logOffset+m.visibleLines())
			if m.logOffset >= max(0, len(m.logs)-m.visibleLines()) {
				m.follow = true
			}
		case "G", "end":
			m.follow = true
			m.logOffset = max(0, len(m.logs)-m.visibleLines())
		case "?":
			m.showHelp = true
		}
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.follow = false
			m.logOffset = max(0, m.logOffset-3)
		case tea.MouseButtonWheelDown:
			m.logOffset = min(max(0, len(m.logs)-m.visibleLines()), m.logOffset+3)
			if m.logOffset >= max(0, len(m.logs)-m.visibleLines()) {
				m.follow = true
			}
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionPress && msg.X < m.leftWidth() && msg.Y >= 2 {
				rows := m.rows()
				index := msg.Y - 2
				if index < len(rows) {
					m.selected = index
					if rows[index].Header != nil {
						m.toggleSection(rows[index].Header)
					} else {
						m.resetLogs()
					}
				}
			}
		}
	}
	return m, nil
}

func (m *attachModel) resetLogs() {
	m.selectionGeneration++
	m.logs = nil
	m.logNext = 0
	m.logName = ""
	m.logOffset = 0
	m.follow = true
}

func (m *attachModel) visibleLines() int {
	return max(1, m.height-5)
}

func (m *attachModel) leftWidth() int {
	w := m.width / 3
	if w < 18 {
		w = 18
	}
	if w > 36 {
		w = 36
	}
	return min(w, max(18, m.width/4))
}

func (m *attachModel) View() string {
	if m.width < 40 || m.height < 8 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render("Terminal too small; resize to at least 40x8")
	}
	if m.showHelp {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.helpView())
	}

	leftWidth := m.leftWidth()
	rightWidth := max(20, m.width-leftWidth-1)
	bodyHeight := max(5, m.height-2)

	left := panelStyle.Width(leftWidth - 3).Height(bodyHeight - 2).Render(m.processList())
	right := panelStyle.Width(rightWidth - 3).Height(bodyHeight - 2).Render(m.logView())
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	footer := m.footerView()
	if m.statusText != "" {
		footer = m.statusText + "  " + footer
	}
	if m.pollErr != "" {
		footer = failedStyle.Render(m.pollErr) + "  " + footer
	}
	return body + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(footer)
}

func (m *attachModel) footerView() string {
	parts := []string{
		keycap("s") + " stop",
		keycap("r") + " restart",
		keycap("f") + " free port",
		keycap("w") + " web",
		keycap("q") + " detach",
		keycap("?") + " help",
	}
	return mutedStyle.Render("attach") + " · " + strings.Join(parts, mutedStyle.Render(" · "))
}

func (m *attachModel) helpView() string {
	rows := [][2]string{
		{"↑/k ↓/j", "select process"},
		{"←/h →/l", "fold section"},
		{"enter", "start"},
		{"s", "stop"},
		{"r", "restart"},
		{"f", "free port"},
		{"space", "mark selected"},
		{"m", "mark all running"},
		{"w", "toggle web logs (copies the URL)"},
		{"W", "toggle word wrap"},
		{"pgup/pgdn", "page logs"},
		{"G / end", "follow bottom"},
		{"q", "detach (leave serve running)"},
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Attach — keyboard"))
	b.WriteString("\n\n")
	for _, row := range rows {
		b.WriteString(keycap(row[0]))
		pad := max(1, 14-ansi.StringWidth(row[0]))
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(row[1])
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("press any key to close · stacker down stops the daemon"))
	return panelStyle.Render(b.String())
}

func (m *attachModel) processList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Processes"))
	b.WriteString(mutedStyle.Render(" · attached"))
	b.WriteString("\n")
	rows := m.rows()
	if len(rows) == 0 {
		b.WriteString(mutedStyle.Render("(none)"))
		return b.String()
	}
	for i, row := range rows {
		if row.Header != nil {
			b.WriteString(formatSectionHeader(
				row.Header.Name,
				m.summarizeSection(row.Header),
				m.collapsed[row.Header.Name],
				i == m.selected,
				max(1, m.leftWidth()-5),
			))
			if i+1 < len(rows) {
				b.WriteByte('\n')
			}
			continue
		}
		if row.Member == nil || row.Member.Index < 0 || row.Member.Index >= len(m.procs) {
			continue
		}
		p := m.procs[row.Member.Index]
		status := p.Status
		kind := ""
		if status == "disabled" {
			kind = "disabled"
		} else if p.Errors > 0 && status != "failed" {
			status += "!"
			kind = "error"
		} else if status == "running" {
			kind = "running"
		} else if status == "failed" {
			kind = "failed"
		}
		name := sanitizeLogLine(p.Name)
		if p.Orphaned {
			name += " ⚠"
		}
		line := formatProcessListLine(processListLine{
			name:         name,
			status:       status,
			statusKind:   kind,
			color:        p.Color,
			oneShot:      p.OneShot,
			hasTasks:     len(p.Tasks) > 0,
			selected:     i == m.selected,
			contentWidth: max(1, m.leftWidth()-5),
		})
		b.WriteString(line)
		if i+1 < len(rows) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m *attachModel) logView() string {
	name := m.currentName()
	currentSection := m.selectedSection()
	title := name
	if currentSection != nil {
		title = currentSection.Name
	}
	if title == "" {
		title = "logs"
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(truncate(title, max(10, m.width-m.leftWidth()-8))))
	b.WriteString("\n")
	if currentSection != nil {
		summary := m.summarizeSection(currentSection)
		parts := []string{fmt.Sprintf("%d/%d services running", summary.Running, summary.Total)}
		if len(currentSection.Tasks) > 0 {
			taskLabel := "tasks"
			if len(currentSection.Tasks) == 1 {
				taskLabel = "task"
			}
			parts = append(parts, fmt.Sprintf("%d %s", len(currentSection.Tasks), taskLabel))
		}
		b.WriteString(mutedStyle.Render(strings.Join(parts, " · ")))
		return b.String()
	}

	vis := m.visibleLines()
	if len(m.logs) == 0 {
		b.WriteString(mutedStyle.Render("(no logs yet)"))
		return b.String()
	}
	start := clamp(m.logOffset, 0, max(0, len(m.logs)-1))
	end := min(len(m.logs), start+vis)
	width := max(10, m.width-m.leftWidth()-6)
	for i := start; i < end; i++ {
		line := sanitizeLogLine(m.logs[i])
		if m.wrap {
			// Simple hard wrap for attach view.
			for len(line) > 0 {
				chunk := truncate(line, width)
				b.WriteString(chunk)
				b.WriteByte('\n')
				if ansi.StringWidth(line) <= width {
					break
				}
				// Drop one visual width chunk approximately.
				if len(line) > width {
					line = line[min(len(line), width):]
				} else {
					break
				}
			}
		} else {
			b.WriteString(truncate(line, width))
			if i+1 < end {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
