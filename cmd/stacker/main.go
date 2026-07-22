package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Version   int                      `yaml:"version"`
	UI        UIConfig                 `yaml:"ui"`
	Processes map[string]ProcessConfig `yaml:"processes"`
}

type UIConfig struct {
	WheelLines    int  `yaml:"wheel_lines"`
	CopyOnRelease bool `yaml:"copy_on_release"`
	MaxLogLines   int  `yaml:"max_log_lines"`
}

type ProcessConfig struct {
	Command         string `yaml:"command"`
	Cwd             string `yaml:"cwd"`
	Autostart       bool   `yaml:"autostart"`
	GracefulTimeout string `yaml:"graceful_timeout"`
	// Port, when set, is freed (listeners killed) before each start/restart.
	Port int `yaml:"port"`
}

type ProcessStatus string

const (
	StatusStopped  ProcessStatus = "stopped"
	StatusStarting ProcessStatus = "starting"
	StatusRunning  ProcessStatus = "running"
	StatusStopping ProcessStatus = "stopping"
	StatusFailed   ProcessStatus = "failed"
)

type Process struct {
	Name   string
	Config ProcessConfig

	mu         sync.Mutex
	cmd        *exec.Cmd
	status     ProcessStatus
	logs       []string
	maxLogs    int
	generation uint64
	done       chan struct{}
}

func NewProcess(name string, cfg ProcessConfig, maxLogs int) *Process {
	if maxLogs <= 0 {
		maxLogs = 10_000
	}
	return &Process{Name: name, Config: cfg, status: StatusStopped, maxLogs: maxLogs}
}

func (p *Process) Status() ProcessStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *Process) Logs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.logs))
	copy(out, p.logs)
	return out
}

func (p *Process) appendLog(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appendLogLocked(line)
}

func (p *Process) appendLogLocked(line string) {
	p.logs = append(p.logs, line)
	if len(p.logs) > p.maxLogs {
		overflow := len(p.logs) - p.maxLogs
		p.logs = append([]string(nil), p.logs[overflow:]...)
	}
}

func (p *Process) Start(notify func()) error {
	p.mu.Lock()
	if p.cmd != nil || p.status == StatusRunning || p.status == StatusStarting || p.status == StatusStopping {
		p.mu.Unlock()
		return nil
	}
	p.status = StatusStarting
	p.generation++
	generation := p.generation
	port := p.Config.Port
	cwd := p.Config.Cwd
	if cwd == "" {
		cwd = "."
	}
	command := p.Config.Command
	p.mu.Unlock()

	if port > 0 {
		killed, err := freePort(port)
		if err != nil {
			p.mu.Lock()
			if p.generation == generation {
				p.startFailedLocked(err)
			}
			p.mu.Unlock()
			notify()
			return err
		}
		if len(killed) > 0 {
			p.appendLog(fmt.Sprintf("[stacker] freed port %d (killed pids=%v)", port, killed))
			notify()
		}
	}

	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		p.mu.Lock()
		if p.generation == generation {
			p.startFailedLocked(err)
		}
		p.mu.Unlock()
		notify()
		return err
	}

	cmd := exec.Command(shellName(), shellRunArg(), command)
	cmd.Dir = absCwd
	cmd.Env = os.Environ()
	setProcGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.mu.Lock()
		if p.generation == generation {
			p.startFailedLocked(err)
		}
		p.mu.Unlock()
		notify()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.mu.Lock()
		if p.generation == generation {
			p.startFailedLocked(err)
		}
		p.mu.Unlock()
		notify()
		return err
	}

	p.mu.Lock()
	if p.generation != generation || p.status != StatusStarting {
		p.mu.Unlock()
		return nil
	}
	if err := cmd.Start(); err != nil {
		p.startFailedLocked(err)
		p.mu.Unlock()
		notify()
		return err
	}

	done := make(chan struct{})
	p.cmd = cmd
	p.done = done
	p.status = StatusRunning
	p.mu.Unlock()
	p.appendLog(fmt.Sprintf("[stacker] started pid=%d", cmd.Process.Pid))
	notify()

	go p.capture(stdout, "stdout", notify)
	go p.capture(stderr, "stderr", notify)
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		if p.generation == generation {
			p.cmd = nil
			p.done = nil
			if p.status != StatusStopping && err != nil {
				p.status = StatusFailed
			} else {
				p.status = StatusStopped
			}
		}
		if err != nil {
			p.appendLogLocked("[stacker] exited: " + err.Error())
		} else {
			p.appendLogLocked("[stacker] exited cleanly")
		}
		p.mu.Unlock()
		close(done)
		notify()
	}()

	return nil
}

func (p *Process) startFailedLocked(err error) {
	p.status = StatusFailed
	p.appendLogLocked("[stacker] start failed: " + err.Error())
}

func (p *Process) capture(r io.Reader, stream string, notify func()) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		p.appendLog(fmt.Sprintf("[%s] %s", stream, sanitizeLogLine(scanner.Text())))
		notify()
	}
	if err := scanner.Err(); err != nil {
		p.appendLog(fmt.Sprintf("[stacker] %s read error: %v", stream, err))
		notify()
	}
}

func (p *Process) Stop(notify func()) error {
	timeout := 8 * time.Second
	if p.Config.GracefulTimeout != "" {
		parsed, err := time.ParseDuration(p.Config.GracefulTimeout)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid graceful timeout %q", p.Config.GracefulTimeout)
		}
		timeout = parsed
	}

	p.mu.Lock()
	cmd := p.cmd
	if cmd == nil || cmd.Process == nil {
		p.status = StatusStopped
		p.mu.Unlock()
		notify()
		return nil
	}
	done := p.done
	alreadyStopping := p.status == StatusStopping
	p.status = StatusStopping
	pid := cmd.Process.Pid
	if !alreadyStopping {
		p.appendLogLocked("[stacker] sending SIGTERM")
	}
	p.mu.Unlock()
	notify()

	if !alreadyStopping {
		if err := signalProcessGroup(pid, signalTerm); err != nil && !errors.Is(err, errProcessNotFound) {
			p.mu.Lock()
			if p.cmd == cmd {
				p.status = StatusRunning
			}
			p.appendLogLocked("[stacker] stop failed: " + err.Error())
			p.mu.Unlock()
			notify()
			return err
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}

	p.appendLog("[stacker] graceful timeout reached; sending SIGKILL")
	if err := signalProcessGroup(pid, signalKill); err != nil && !errors.Is(err, errProcessNotFound) {
		return err
	}
	<-done
	return nil
}

// Mark appends a visual separator to the logs so everything after it is
// clearly "new" (blank line, timestamped rule, blank line).
func (p *Process) Mark() {
	stamp := time.Now().Format("15:04:05")
	p.mu.Lock()
	p.appendLogLocked("")
	p.appendLogLocked(fmt.Sprintf("────────── mark %s ──────────", stamp))
	p.appendLogLocked("")
	p.mu.Unlock()
}

func (p *Process) Restart(notify func()) {
	go func() {
		if err := p.Stop(notify); err != nil {
			p.appendLog("[stacker] restart failed while stopping: " + err.Error())
			notify()
			return
		}
		_ = p.Start(notify)
	}()
}

type refreshMsg struct{}
type copiedMsg struct {
	lines int
	err   error
}
type webMsg struct {
	url string
	err error
}

type model struct {
	cfg       Config
	processes []*Process
	selected  int
	width     int
	height    int
	logOffset int
	follow    bool

	selecting bool
	selStart  int
	selEnd    int

	statusText string
	refreshCh  chan struct{}
	configPath string
	web        *webServer
}

var (
	panelStyle           = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	titleStyle           = lipgloss.NewStyle().Bold(true)
	mutedStyle           = lipgloss.NewStyle().Faint(true)
	selectedProcessStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	selectionStyle       = lipgloss.NewStyle().Reverse(true)
	runningStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	failedStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func newModel(cfg Config) *model {
	if cfg.UI.WheelLines <= 0 {
		cfg.UI.WheelLines = 3
	}
	m := &model{cfg: cfg, selected: -1, follow: true, selStart: -1, selEnd: -1, refreshCh: make(chan struct{}, 1)}
	names := make([]string, 0, len(cfg.Processes))
	for name := range cfg.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.processes = append(m.processes, NewProcess(name, cfg.Processes[name], cfg.UI.MaxLogLines))
		if m.selected == -1 && cfg.Processes[name].Autostart {
			m.selected = len(m.processes) - 1
		}
	}
	if m.selected == -1 && len(m.processes) > 0 {
		m.selected = 0
	}
	return m
}

func (m *model) notify() {
	select {
	case m.refreshCh <- struct{}{}:
	default:
	}
}

func (m *model) waitRefresh() tea.Cmd {
	return func() tea.Msg {
		<-m.refreshCh
		return refreshMsg{}
	}
}

func (m *model) Init() tea.Cmd {
	for _, p := range m.processes {
		if p.Config.Autostart {
			go func(proc *Process) { _ = proc.Start(m.notify) }(p)
		}
	}
	return m.waitRefresh()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case refreshMsg:
		if m.follow {
			m.scrollToBottom()
		}
		return m, m.waitRefresh()
	case copiedMsg:
		switch {
		case msg.err != nil:
			m.statusText = "Copy failed: " + msg.err.Error()
		case msg.lines > 0:
			m.statusText = fmt.Sprintf("Copied %d line(s)", msg.lines)
		default:
			m.statusText = "Nothing selected to copy"
		}
	case webMsg:
		if msg.err != nil {
			m.statusText = "Web logs: " + msg.url + " (browser failed: " + msg.err.Error() + ")"
		} else {
			m.statusText = "Web logs: " + msg.url + " (URL copied, browser opened)"
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q":
			return m, m.stopAllCmd()
		case "ctrl+c":
			if m.hasSelection() {
				return m, m.copySelectionCmd()
			}
			return m, m.stopAllCmd()
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				m.resetLogView()
			}
		case "down", "j":
			if m.selected+1 < len(m.processes) {
				m.selected++
				m.resetLogView()
			}
		case "s":
			if p := m.current(); p != nil {
				go func() { _ = p.Stop(m.notify) }()
			}
		case "enter":
			if p := m.current(); p != nil {
				go func() { _ = p.Start(m.notify) }()
			}
		case "r":
			if p := m.current(); p != nil {
				p.Restart(m.notify)
			}
		case " ":
			if p := m.current(); p != nil {
				p.Mark()
				m.notify()
			}
		case "f":
			if p := m.current(); p != nil && p.Config.Port > 0 {
				go func(proc *Process) {
					killed, err := freePort(proc.Config.Port)
					if err != nil {
						proc.appendLog("[stacker] free-port failed: " + err.Error())
					} else if len(killed) == 0 {
						proc.appendLog(fmt.Sprintf("[stacker] port %d: nothing listening", proc.Config.Port))
					} else {
						proc.appendLog(fmt.Sprintf("[stacker] freed port %d (killed pids=%v)", proc.Config.Port, killed))
					}
					m.notify()
				}(p)
			}
		case "w":
			if m.web != nil {
				ws := m.web
				m.web = nil
				m.statusText = "Web logs stopped"
				go ws.Close()
				break
			}
			ws, err := startWebServer(m, m.configPath)
			if err != nil {
				m.statusText = "Web logs failed: " + err.Error()
				break
			}
			m.web = ws
			target := "http://" + ws.Addr() + "/"
			if p := m.current(); p != nil {
				target = webLogsURL(ws.Addr(), p.Name)
			}
			return m, m.openWebCmd(target)
		case "pgup":
			m.scrollLogs(-m.visibleLogLines())
		case "pgdown":
			m.scrollLogs(m.visibleLogLines())
		case "end", "G":
			m.follow = true
			m.scrollToBottom()
		case "esc":
			m.clearSelection()
		}
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m *model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	leftWidth := m.leftWidth()
	logX := leftWidth + 1
	logTop := 2
	logBottom := logTop + m.visibleLogLines() - 1
	insideLogs := msg.X >= logX && msg.Y >= logTop && msg.Y <= logBottom

	if msg.Action == tea.MouseActionPress {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if insideLogs {
				m.scrollLogs(-m.cfg.UI.WheelLines)
			}
		case tea.MouseButtonWheelDown:
			if insideLogs {
				m.scrollLogs(m.cfg.UI.WheelLines)
			}
		case tea.MouseButtonLeft:
			if insideLogs {
				line := m.mouseLogLine(msg.Y)
				m.selecting = true
				m.selStart, m.selEnd = line, line
			} else if msg.X < leftWidth && msg.Y > 0 {
				idx := msg.Y - 2
				if idx >= 0 && idx < len(m.processes) {
					m.selected = idx
					m.resetLogView()
				}
			}
		}
	}

	if msg.Action == tea.MouseActionMotion && m.selecting {
		line := m.mouseLogLine(clamp(msg.Y, logTop, logBottom))
		m.selEnd = line
		if msg.Y < logTop {
			m.scrollLogs(-1)
		} else if msg.Y > logBottom {
			m.scrollLogs(1)
		}
	}

	if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft && m.selecting {
		m.selecting = false
		if m.cfg.UI.CopyOnRelease && m.hasSelection() {
			return m, m.copySelectionCmd()
		}
	}
	return m, nil
}

func (m *model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}
	if m.width < 40 || m.height < 8 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render("Terminal too small; resize to at least 40x8")
	}
	leftWidth := m.leftWidth()
	rightWidth := max(20, m.width-leftWidth-1)
	bodyHeight := max(5, m.height-2)

	left := panelStyle.Width(leftWidth - 3).Height(bodyHeight - 2).Render(m.processList())
	right := panelStyle.Width(rightWidth - 3).Height(bodyHeight - 2).Render(m.logView())
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	footer := mutedStyle.Render("Enter start • s stop • r restart • f free-port • w web logs • space mark • wheel scroll • drag copy • G bottom • q quit")
	if m.statusText != "" {
		footer = m.statusText + "  " + footer
	}
	return body + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(footer)
}

func (m *model) processList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Processes"))
	b.WriteString("\n")
	for i, p := range m.processes {
		processStatus := p.Status()
		status := string(processStatus)
		name := sanitizeLogLine(p.Name)
		statusRendered := status
		if processStatus == StatusRunning {
			statusRendered = runningStyle.Render(status)
		} else if processStatus == StatusFailed {
			statusRendered = failedStyle.Render(status)
		}
		contentWidth := max(1, m.leftWidth()-5)
		nameWidth := max(1, contentWidth-len(status)-1)
		name = truncate(name, nameWidth)
		line := name + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + " " + statusRendered
		if i == m.selected {
			line = selectedProcessStyle.Render(line)
		}
		b.WriteString(line)
		if i+1 < len(m.processes) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m *model) logView() string {
	p := m.current()
	if p == nil {
		return "No processes configured"
	}
	logs := p.Logs()
	visibleLines := m.visibleLogLines()
	maxOffset := max(0, len(logs)-visibleLines)
	m.logOffset = clamp(m.logOffset, 0, maxOffset)
	end := min(len(logs), m.logOffset+visibleLines)

	var b strings.Builder
	title := fmt.Sprintf("Logs: %s [%s]", sanitizeLogLine(p.Name), p.Status())
	if !m.follow {
		title += " — paused; press G for bottom"
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteByte('\n')
	for i := m.logOffset; i < end; i++ {
		line := truncate(logs[i], max(10, m.width-m.leftWidth()-6))
		if m.isSelected(i) {
			line = selectionStyle.Render(line)
		}
		b.WriteString(line)
		if i+1 < end {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m *model) current() *Process {
	if m.selected < 0 || m.selected >= len(m.processes) {
		return nil
	}
	return m.processes[m.selected]
}

func (m *model) leftWidth() int {
	if m.width < 70 {
		return max(24, m.width/3)
	}
	return min(34, m.width/3)
}

func (m *model) logHeight() int { return max(3, m.height-5) }

func (m *model) visibleLogLines() int { return max(1, m.logHeight()-1) }

func (m *model) mouseLogLine(y int) int {
	return max(0, m.logOffset+(y-2))
}

func (m *model) scrollLogs(delta int) {
	p := m.current()
	if p == nil {
		return
	}
	logs := p.Logs()
	maxOffset := max(0, len(logs)-m.visibleLogLines())
	m.logOffset = clamp(m.logOffset+delta, 0, maxOffset)
	m.follow = m.logOffset >= maxOffset
}

func (m *model) scrollToBottom() {
	p := m.current()
	if p == nil {
		return
	}
	m.logOffset = max(0, len(p.Logs())-m.visibleLogLines())
}

func (m *model) resetLogView() {
	m.clearSelection()
	m.follow = true
	m.scrollToBottom()
}

func (m *model) hasSelection() bool { return m.selStart >= 0 && m.selEnd >= 0 }

func (m *model) isSelected(line int) bool {
	if !m.hasSelection() {
		return false
	}
	start, end := m.selStart, m.selEnd
	if start > end {
		start, end = end, start
	}
	return line >= start && line <= end
}

func (m *model) clearSelection() {
	m.selecting = false
	m.selStart, m.selEnd = -1, -1
}

func (m *model) selectedText() (string, int) {
	p := m.current()
	if p == nil || !m.hasSelection() {
		return "", 0
	}
	logs := p.Logs()
	start, end := m.selStart, m.selEnd
	if start > end {
		start, end = end, start
	}
	start = clamp(start, 0, max(0, len(logs)-1))
	end = clamp(end, 0, max(0, len(logs)-1))
	if len(logs) == 0 || start > end {
		return "", 0
	}
	return strings.Join(logs[start:end+1], "\n"), end - start + 1
}

func (m *model) copySelectionCmd() tea.Cmd {
	text, count := m.selectedText()
	return func() tea.Msg {
		if text == "" {
			return copiedMsg{lines: 0}
		}
		if err := copyToClipboard(text); err != nil {
			return copiedMsg{lines: 0, err: err}
		}
		return copiedMsg{lines: count}
	}
}

func (m *model) openWebCmd(target string) tea.Cmd {
	return func() tea.Msg {
		// Copy the URL so it can be pasted even if no browser opens (e.g. SSH).
		_ = copyToClipboard(target)
		if err := openBrowser(target); err != nil {
			return webMsg{url: target, err: err}
		}
		return webMsg{url: target}
	}
}

func (m *model) stopAllCmd() tea.Cmd {
	return func() tea.Msg {
		m.stopAll()
		return tea.Quit()
	}
}

func (m *model) stopAll() {
	var wg sync.WaitGroup
	for _, p := range m.processes {
		wg.Add(1)
		go func(proc *Process) {
			defer wg.Done()
			_ = proc.Stop(func() {})
		}(p)
	}
	wg.Wait()
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("multiple YAML documents are not supported")
		}
		return Config{}, err
	}
	if cfg.Version != 1 {
		return Config{}, fmt.Errorf("unsupported config version %d; expected 1", cfg.Version)
	}
	if len(cfg.Processes) == 0 {
		return Config{}, errors.New("no processes configured")
	}
	if cfg.UI.WheelLines < 0 {
		return Config{}, errors.New("ui.wheel_lines cannot be negative")
	}
	if cfg.UI.MaxLogLines < 0 {
		return Config{}, errors.New("ui.max_log_lines cannot be negative")
	}

	configDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Config{}, err
	}
	for name, processCfg := range cfg.Processes {
		if strings.TrimSpace(name) == "" {
			return Config{}, errors.New("process name cannot be empty")
		}
		if strings.TrimSpace(processCfg.Command) == "" {
			return Config{}, fmt.Errorf("process %q command cannot be empty", name)
		}
		if processCfg.GracefulTimeout != "" {
			timeout, err := time.ParseDuration(processCfg.GracefulTimeout)
			if err != nil || timeout <= 0 {
				return Config{}, fmt.Errorf("process %q has invalid graceful_timeout %q", name, processCfg.GracefulTimeout)
			}
		}
		if processCfg.Port < 0 || processCfg.Port > 65535 {
			return Config{}, fmt.Errorf("process %q has invalid port %d", name, processCfg.Port)
		}
		if processCfg.Cwd == "" {
			processCfg.Cwd = "."
		}
		if !filepath.IsAbs(processCfg.Cwd) {
			processCfg.Cwd = filepath.Join(configDir, processCfg.Cwd)
		}
		processCfg.Cwd = filepath.Clean(processCfg.Cwd)
		info, err := os.Stat(processCfg.Cwd)
		if err != nil {
			return Config{}, fmt.Errorf("process %q cwd: %w", name, err)
		}
		if !info.IsDir() {
			return Config{}, fmt.Errorf("process %q cwd %q is not a directory", name, processCfg.Cwd)
		}
		cfg.Processes[name] = processCfg
	}
	return cfg, nil
}

func main() {
	configPath, rest := parseArgs(os.Args[1:])
	if len(rest) > 0 {
		os.Exit(runCLI(configPath, rest))
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	m := newModel(cfg)
	control, err := startControlServer(m, configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control plane error:", err)
		os.Exit(1)
	}
	defer control.Close()
	m.configPath = control.config

	program := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
		tea.WithContext(ctx),
	)
	_, runErr := program.Run()
	if m.web != nil {
		m.web.Close()
	}
	m.stopAll()
	if runErr != nil && !(ctx.Err() != nil && errors.Is(runErr, tea.ErrProgramKilled)) {
		fmt.Fprintln(os.Stderr, "stacker error:", runErr)
		os.Exit(1)
	}
}

// parseArgs extracts -config and leaves CLI subcommands in rest.
// Examples: stacker list --json | stacker -config app.yml restart api
func parseArgs(args []string) (configPath string, rest []string) {
	configPath = "stacker.yml"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			// bare help without subcommand → CLI help when no TUI intent
			rest = append(rest, "help")
		case a == "-config" || a == "--config":
			if i+1 < len(args) {
				i++
				configPath = args[i]
			}
		case strings.HasPrefix(a, "-config="):
			configPath = strings.TrimPrefix(a, "-config=")
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
		case a == "-json" || a == "--json":
			rest = append(rest, a)
		default:
			// stop treating leading flags after first non-flag for simplicity
			if strings.HasPrefix(a, "-") && len(rest) == 0 {
				// unknown root flag — keep for flag package? ignore and pass through
				rest = append(rest, a)
				continue
			}
			rest = append(rest, a)
		}
	}
	return configPath, rest
}

func clamp(v, low, high int) int {
	if high < low {
		return low
	}
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func sanitizeLogLine(line string) string {
	line = ansi.Strip(line)
	var b strings.Builder
	b.Grow(len(line))
	for _, r := range line {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case unicode.IsControl(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
