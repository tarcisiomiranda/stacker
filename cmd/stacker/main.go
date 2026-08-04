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
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"runtime/debug"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"
)

// version is stamped by the release build via -ldflags "-X main.version=...".
var version = "dev"

// resolveVersion falls back to Go build info so `go install` and local builds
// still report something traceable.
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
		var rev, dirty string
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return "dev+" + rev + dirty
		}
	}
	return version
}

type Config struct {
	Version   int                      `yaml:"version"`
	UI        UIConfig                 `yaml:"ui"`
	Processes map[string]ProcessConfig `yaml:"processes"`
	// Tasks are standalone one-shot commands, not tied to any process. Each
	// gets its own entry in the list and its own log; running one executes
	// the command once and it returns to idle instead of staying up.
	Tasks map[string]TaskConfig `yaml:"tasks"`

	// processOrder / taskOrder are the YAML key orders; they define the
	// display order in the TUI and web viewer.
	processOrder []string
	taskOrder    []string
}

// TaskConfig is a standalone one-shot command (root-level `tasks:`).
type TaskConfig struct {
	Command string `yaml:"command"`
	Cwd     string `yaml:"cwd"`
	Color   string `yaml:"color"`
}

type UIConfig struct {
	WheelLines    int  `yaml:"wheel_lines"`
	CopyOnRelease bool `yaml:"copy_on_release"`
	MaxLogLines   int  `yaml:"max_log_lines"`
	// WordWrap is the initial wrap state for logs in the TUI and web viewer;
	// both can toggle it at runtime (TUI key `W`, web checkbox).
	WordWrap bool `yaml:"word_wrap"`
	// HighlightErrors enables error detection on captured output: lines that
	// look like errors (tracebacks, panics, ERROR levels) turn the process
	// badge orange in the TUI and web viewer even while it keeps running.
	// Off by default; the cost is one regex match per log line.
	HighlightErrors bool `yaml:"highlight_errors"`
	// WebHost is the bind address for the on-demand web log viewer (key `w`).
	// Empty means 0.0.0.0 so remote browsers can reach a server-side Stacker.
	WebHost string `yaml:"web_host"`
	// WebPort is the TCP port for the web viewer. Empty/0 means 52911.
	// Set to a free high port if 52911 is taken; the listener falls back to an
	// ephemeral port when the preferred one is busy.
	WebPort int `yaml:"web_port"`
}

type ProcessConfig struct {
	Command         string `yaml:"command"`
	Cwd             string `yaml:"cwd"`
	Autostart       bool   `yaml:"autostart"`
	GracefulTimeout string `yaml:"graceful_timeout"`
	// Port, when set, is freed (listeners killed) before each start/restart.
	Port int `yaml:"port"`
	// Color, when set, draws a colored dot next to the process name (TUI list
	// and web sidebar) for visual grouping. Hex (#0af, #00aaff) or CSS name.
	Color string `yaml:"color"`
	// Tasks are named one-shot commands (migrations, seeds, cache clears) run
	// on demand in the process's working directory. They stream into the
	// process log and do not affect its status: the service keeps running.
	Tasks map[string]string `yaml:"tasks"`
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
	// oneShot marks a standalone task entry: it is expected to run once and
	// exit, so a clean exit reads as "idle", not a stopped service, and it is
	// excluded from process-only features (reorder, color persistence).
	oneShot bool
	// orphaned is set when the process was removed from stacker.yml while
	// still running; it stays listed until it stops, then is pruned.
	orphaned bool

	mu sync.Mutex
	// detectErrors mirrors ui.highlight_errors; toggled at runtime via the
	// web checkbox (SetDetectErrors).
	detectErrors bool
	cmd          *exec.Cmd
	status       ProcessStatus
	logs         []string
	maxLogs      int
	// errCount is the number of error-looking output lines since the last
	// start or mark (mark = user acknowledged them).
	errCount int
	// runningTasks tracks one-shot tasks in flight so the same task name is
	// not launched twice concurrently.
	runningTasks map[string]struct{}
	// dropped is the absolute index of logs[0]: lines trimmed by the memory
	// cap. Lets clients tail incrementally with stable absolute offsets.
	dropped    int
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

// Color is the only Config field mutated at runtime (TUI `c` / web selector),
// so reads and writes go through the mutex.
func (p *Process) Color() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Config.Color
}

func (p *Process) SetColor(c string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Config.Color = c
}

// errLineRe matches output lines that look like errors: Python tracebacks and
// exceptions, Go panics, JS/TS errors, npm/Rust/log-level markers. Matched per
// line at capture time, only when ui.highlight_errors is on.
var errLineRe = regexp.MustCompile(`Traceback \(most recent call last\)|\bpanic:|\bfatal error:|\b[A-Z][A-Za-z]+(?:Error|Exception)\b|\bError:|\bERROR\b|\berror(?::|\[)|\bERR!|\bFATAL\b|\bCRITICAL\b|\bUnhandled(?:PromiseRejection|Rejection)\b`)

// Errors is the count of error-looking lines since the last start/mark.
func (p *Process) Errors() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errCount
}

// SetDetectErrors toggles error detection at runtime (web checkbox). Turning
// it off also clears the current badge.
func (p *Process) SetDetectErrors(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.detectErrors = on
	if !on {
		p.errCount = 0
	}
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
		p.dropped += overflow
		p.logs = append([]string(nil), p.logs[overflow:]...)
	}
}

// TailLogs returns lines from absolute index `from` on, plus the actual start
// (>= from when lines were trimmed) and the next absolute index to ask for.
func (p *Process) TailLogs(from int) (start int, lines []string, next int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next = p.dropped + len(p.logs)
	from = clamp(from, p.dropped, next)
	lines = make([]string, next-from)
	copy(lines, p.logs[from-p.dropped:])
	return from, lines, next
}

// LogNext returns the next absolute log index without copying any lines.
func (p *Process) LogNext() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped + len(p.logs)
}

func (p *Process) Start(notify func()) error {
	p.mu.Lock()
	if p.cmd != nil || p.status == StatusRunning || p.status == StatusStarting || p.status == StatusStopping {
		p.mu.Unlock()
		return nil
	}
	p.status = StatusStarting
	p.errCount = 0
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
		} else {
			p.appendLog(fmt.Sprintf("[stacker] port %d free", port))
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

// recordLine appends a display line to the log and, when error detection is
// on, bumps the badge if matchText looks like an error. matchText is the raw
// content without the stream/task prefix so the prefix never triggers a match.
func (p *Process) recordLine(display, matchText string) {
	p.mu.Lock()
	p.appendLogLocked(display)
	if p.detectErrors && matchText != "" && errLineRe.MatchString(matchText) {
		p.errCount++
	}
	p.mu.Unlock()
}

func (p *Process) capture(r io.Reader, stream string, notify func()) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		text := sanitizeLogLine(scanner.Text())
		p.recordLine("["+stream+"] "+text, text)
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
	// Marking acknowledges past errors: the badge clears until a new one.
	p.errCount = 0
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

// TaskRunning reports whether the named one-shot task is currently executing.
func (p *Process) TaskRunning(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, running := p.runningTasks[name]
	return running
}

// RunTask launches a named one-shot command in the process's working
// directory. Output streams into the process log prefixed with the task name;
// the process status is untouched, so the long-running service keeps running.
// A second call for a task already in flight is ignored. Returns an error only
// for an unknown task; execution outcome is reported through the log.
func (p *Process) RunTask(name string, notify func()) error {
	p.mu.Lock()
	command, ok := p.Config.Tasks[name]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("unknown task %q for process %q", name, p.Name)
	}
	if p.runningTasks == nil {
		p.runningTasks = map[string]struct{}{}
	}
	if _, busy := p.runningTasks[name]; busy {
		p.mu.Unlock()
		return nil
	}
	p.runningTasks[name] = struct{}{}
	cwd := p.Config.Cwd
	if cwd == "" {
		cwd = "."
	}
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.runningTasks, name)
			p.mu.Unlock()
			notify()
		}()

		prefix := "[task " + name + "] "
		absCwd, err := filepath.Abs(cwd)
		if err != nil {
			p.recordLine(prefix+"failed: "+err.Error(), "error:")
			notify()
			return
		}
		p.appendLog(prefix + "$ " + command)
		notify()

		cmd := exec.Command(shellName(), shellRunArg(), command)
		cmd.Dir = absCwd
		cmd.Env = os.Environ()
		setProcGroup(cmd)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			p.recordLine(prefix+"failed: "+err.Error(), "error:")
			notify()
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			p.recordLine(prefix+"failed: "+err.Error(), "error:")
			notify()
			return
		}
		if err := cmd.Start(); err != nil {
			p.recordLine(prefix+"start failed: "+err.Error(), "error:")
			notify()
			return
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); p.captureTask(name, stdout, notify) }()
		go func() { defer wg.Done(); p.captureTask(name, stderr, notify) }()
		wg.Wait()

		if err := cmd.Wait(); err != nil {
			p.mu.Lock()
			p.appendLogLocked(prefix + "exited: " + err.Error())
			if p.detectErrors {
				p.errCount++
			}
			p.mu.Unlock()
		} else {
			p.appendLog(prefix + "done")
		}
		notify()
	}()
	return nil
}

func (p *Process) captureTask(name string, r io.Reader, notify func()) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	prefix := "[task " + name + "] "
	for scanner.Scan() {
		text := sanitizeLogLine(scanner.Text())
		p.recordLine(prefix+text, text)
		notify()
	}
}

type refreshMsg struct{}
type copiedMsg struct {
	lines int
	err   error
}
type webMsg struct {
	url            string
	err            error
	skippedBrowser bool // true when no GUI / xdg-open skipped (SSH servers)
}
type colorSavedMsg struct {
	name  string
	color string
	err   error
}
type orderSavedMsg struct{ err error }

type model struct {
	cfg       Config
	processes []*Process
	selected  int
	width     int
	height    int
	logOffset int
	follow    bool
	wrap      bool
	showHelp  bool
	showTasks bool

	selecting bool
	selStart  int
	selEnd    int

	statusText string
	refreshCh  chan struct{}
	configPath string
	// webMu guards web: the TUI `w` key, the control plane (/v1/web) and
	// shutdown all toggle the viewer.
	webMu sync.Mutex
	web   *webServer

	// procsMu guards the processes slice reference: the TUI goroutine swaps
	// it on reorder while web/control handlers iterate it.
	procsMu sync.RWMutex
	// pendingOrder is a reorder requested by the web viewer, applied by the
	// TUI goroutine on the next refresh (it owns `selected`).
	pendingMu    sync.Mutex
	pendingOrder []string
	// pendingConfig is a reloaded stacker.yml applied on the next refresh.
	pendingConfig     *Config
	pendingConfigHash string
	pendingConfigErr  string
	// configHash is the last successfully applied file content hash.
	hashMu     sync.Mutex
	configHash string
	// watchStop ends the config file poller.
	watchStop chan struct{}
	// hlErr mirrors ui.highlight_errors; toggled at runtime from the web.
	hlErr atomic.Bool
	// numProcesses is the count of real (service) processes at the front of
	// m.processes (YAML order, non-orphaned); orphaned services and
	// standalone tasks follow. Reorder and color persistence apply only to
	// this prefix.
	numProcesses int
	// mode is "session" (TUI owns lifecycle) or "serve" (headless daemon).
	mode string
	// shutdown is closed to request supervisor exit (serve or /v1/down).
	shutdown chan struct{}
	// attachMode is true when this TUI is a remote attach client (q detaches).
	attachMode bool
}

// orderedNames returns the display order: the YAML key order when known,
// alphabetical otherwise.
func orderedNames(cfg Config) []string {
	if len(cfg.processOrder) == len(cfg.Processes) {
		ok := true
		for _, name := range cfg.processOrder {
			if _, exists := cfg.Processes[name]; !exists {
				ok = false
				break
			}
		}
		if ok {
			return cfg.processOrder
		}
	}
	names := make([]string, 0, len(cfg.Processes))
	for name := range cfg.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// procs returns the current process list; safe from any goroutine.
func (m *model) procs() []*Process {
	m.procsMu.RLock()
	defer m.procsMu.RUnlock()
	return m.processes
}

var (
	panelStyle           = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	titleStyle           = lipgloss.NewStyle().Bold(true)
	mutedStyle           = lipgloss.NewStyle().Faint(true)
	selectedProcessStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	selectionStyle       = lipgloss.NewStyle().Reverse(true)
	runningStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	failedStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	errorBadgeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	// keycapStyle is the keyboard-key chip used in the footer and help overlay.
	keycapStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("238")).
			Bold(true).
			Padding(0, 1)
	// helpSectionStyle labels groups inside the help overlay.
	helpSectionStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("110")).
				Bold(true)
)

func newModel(cfg Config) *model {
	if cfg.UI.WheelLines <= 0 {
		cfg.UI.WheelLines = 3
	}
	m := &model{
		cfg:       cfg,
		selected:  -1,
		follow:    true,
		wrap:      cfg.UI.WordWrap,
		selStart:  -1,
		selEnd:    -1,
		refreshCh: make(chan struct{}, 1),
		watchStop: make(chan struct{}),
		shutdown:  make(chan struct{}),
		mode:      "session",
	}
	m.hlErr.Store(cfg.UI.HighlightErrors)
	names := orderedNames(cfg)
	for _, name := range names {
		p := NewProcess(name, cfg.Processes[name], cfg.UI.MaxLogLines)
		p.detectErrors = cfg.UI.HighlightErrors
		m.processes = append(m.processes, p)
		if m.selected == -1 && cfg.Processes[name].Autostart {
			m.selected = len(m.processes) - 1
		}
	}
	m.numProcesses = len(m.processes)

	// Standalone one-shot tasks follow the processes, in YAML order.
	for _, name := range orderedTaskNames(cfg) {
		tc := cfg.Tasks[name]
		p := NewProcess(name, ProcessConfig{Command: tc.Command, Cwd: tc.Cwd, Color: tc.Color}, cfg.UI.MaxLogLines)
		p.oneShot = true
		p.detectErrors = cfg.UI.HighlightErrors
		m.processes = append(m.processes, p)
	}

	if m.selected == -1 && len(m.processes) > 0 {
		m.selected = 0
	}
	return m
}

// orderedTaskNames returns the standalone task display order: YAML key order
// when known, alphabetical otherwise.
func orderedTaskNames(cfg Config) []string {
	if len(cfg.taskOrder) == len(cfg.Tasks) {
		ok := true
		for _, name := range cfg.taskOrder {
			if _, exists := cfg.Tasks[name]; !exists {
				ok = false
				break
			}
		}
		if ok {
			return cfg.taskOrder
		}
	}
	return sortedTaskConfigNames(cfg.Tasks)
}

func sortedTaskConfigNames(tasks map[string]TaskConfig) []string {
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		m.applyPendingOrder()
		m.applyPendingConfig()
		m.pruneOrphans()
		if m.follow {
			m.scrollToBottom()
		}
		return m, m.waitRefresh()
	case orderSavedMsg:
		if msg.err != nil {
			m.statusText = "Order save failed: " + msg.err.Error()
		} else {
			m.statusText = "Order saved to YAML"
		}
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
		switch {
		case msg.err != nil:
			// Keep the URL front and center — on servers xdg-open is expected to fail.
			m.statusText = "Web logs: " + msg.url + " (URL copied; open in browser — " + msg.err.Error() + ")"
		case msg.skippedBrowser:
			m.statusText = "Web logs: " + msg.url + " (URL copied; open in your browser)"
		default:
			m.statusText = "Web logs: " + msg.url + " (URL copied, browser opened)"
		}
	case colorSavedMsg:
		if msg.err != nil {
			m.statusText = "Color save failed: " + msg.err.Error()
		} else if msg.color == "" {
			m.statusText = fmt.Sprintf("Color removed from %s (saved to YAML)", msg.name)
		} else {
			m.statusText = fmt.Sprintf("Color %s set on %s (saved to YAML)", msg.color, msg.name)
		}
	case tea.KeyMsg:
		// Help overlay swallows every key: first press closes it.
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		// Task picker: digits run a task, anything else closes it.
		if m.showTasks {
			return m, m.handleTaskKey(msg.String())
		}
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
		case "m":
			n := m.markAllRunning()
			m.statusText = fmt.Sprintf("Marked %d running process(es)", n)
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
			if m.stopWeb() {
				m.statusText = "Web logs stopped"
				break
			}
			addr, err := m.startWeb()
			if err != nil {
				m.statusText = "Web logs failed: " + err.Error()
				break
			}
			target := webPublicBaseURL(addr) + "/"
			if p := m.current(); p != nil {
				target = webLogsURL(addr, p.Name)
			}
			return m, m.openWebCmd(target)
		case "shift+up", "K":
			return m, m.moveSelectedCmd(-1)
		case "shift+down", "J":
			return m, m.moveSelectedCmd(1)
		case "W":
			m.wrap = !m.wrap
			if m.follow {
				m.scrollToBottom()
			}
			if m.wrap {
				m.statusText = "Word wrap on"
			} else {
				m.statusText = "Word wrap off"
			}
		case "c":
			return m, m.cycleColorCmd()
		case "t":
			if p := m.current(); p != nil {
				if len(p.Config.Tasks) == 0 {
					m.statusText = "No tasks configured for " + p.Name
				} else {
					m.showTasks = true
				}
			}
		case "?":
			m.showHelp = true
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

	if m.showHelp {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.helpView())
	}
	if m.showTasks {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.tasksView())
	}

	left := panelStyle.Width(leftWidth - 3).Height(bodyHeight - 2).Render(m.processList())
	right := panelStyle.Width(rightWidth - 3).Height(bodyHeight - 2).Render(m.logView())
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Primary actions stay fixed in the footer so stop/restart/free-port/web
	// are always one glance away; full map lives under `?`.
	footer := m.footerView()
	if m.statusText != "" {
		footer = m.statusText + "  " + footer
	}
	return body + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(footer)
}

// keycap renders a keyboard shortcut as a small chip, e.g. [s].
func keycap(key string) string {
	return keycapStyle.Render(key)
}

// footerView is the always-visible primary-action bar.
func (m *model) footerView() string {
	// Compact chips for the four fixed process actions + help/quit.
	parts := []string{
		keycap("s") + " stop",
		keycap("r") + " restart",
		keycap("f") + " free port",
		keycap("w") + " web",
		keycap("?") + " help",
		keycap("q") + " quit",
	}
	return strings.Join(parts, mutedStyle.Render(" · "))
}

// helpView is the full key reference with keycap chips, grouped by role.
func (m *model) helpView() string {
	type section struct {
		title string
		rows  [][2]string
	}
	sections := []section{
		{
			title: "Primary",
			rows: [][2]string{
				{"s", "stop selected process"},
				{"r", "restart selected process"},
				{"f", "free configured port"},
				{"w", "web log viewer on/off"},
			},
		},
		{
			title: "Process",
			rows: [][2]string{
				{"↑/k ↓/j", "select process"},
				{"shift+↑/↓", "move process (saved to YAML)"},
				{"enter", "start (▶ tasks: run once)"},
				{"t", "run a task (one-shot command)"},
				{"c", "cycle color (saved to YAML)"},
			},
		},
		{
			title: "Logs",
			rows: [][2]string{
				{"space", "mark selected"},
				{"m", "mark all running"},
				{"W", "toggle word wrap"},
				{"wheel", "scroll logs"},
				{"drag", "select lines (copy)"},
				{"pgup/pgdn", "page logs"},
				{"G / end", "follow bottom"},
				{"esc", "clear selection"},
			},
		},
		{
			title: "App",
			rows: [][2]string{
				{"?", "this help"},
				{"q / ctrl+c", "quit"},
			},
		},
	}

	// Pad keycaps to a shared width so descriptions line up.
	keyWidth := 0
	for _, sec := range sections {
		for _, row := range sec.rows {
			if w := ansi.StringWidth(row[0]); w > keyWidth {
				keyWidth = w
			}
		}
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Keyboard shortcuts"))
	b.WriteString("\n")
	for i, sec := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(helpSectionStyle.Render(sec.title))
		b.WriteString("\n")
		for _, row := range sec.rows {
			pad := max(0, keyWidth-ansi.StringWidth(row[0]))
			// Extra spaces go after the keycap so chips stay tight.
			b.WriteString(keycap(row[0]))
			b.WriteString(strings.Repeat(" ", pad+2))
			b.WriteString(row[1])
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("press any key to close"))
	return panelStyle.Render(b.String())
}

// sortedTaskNames returns the task names in stable alphabetical order.
func sortedTaskNames(tasks map[string]string) []string {
	if len(tasks) == 0 {
		return nil
	}
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// currentTaskNames returns the selected process's task names, sorted, so the
// picker numbering is stable.
func (m *model) currentTaskNames() []string {
	p := m.current()
	if p == nil {
		return nil
	}
	return sortedTaskNames(p.Config.Tasks)
}

// tasksView is the one-shot task picker overlay for the selected process.
func (m *model) tasksView() string {
	p := m.current()
	if p == nil {
		return panelStyle.Render("No process selected")
	}
	names := m.currentTaskNames()
	var b strings.Builder
	b.WriteString(titleStyle.Render("Tasks — " + sanitizeLogLine(p.Name)))
	b.WriteString("\n\n")
	for i, name := range names {
		marker := " "
		if p.TaskRunning(name) {
			marker = runningStyle.Render("●")
		}
		num := "  "
		if i < 9 {
			num = fmt.Sprintf("%d ", i+1)
		}
		cmd := truncate(sanitizeLogLine(p.Config.Tasks[name]), 40)
		b.WriteString(fmt.Sprintf("%s %s %s  %s\n", num, marker, name, mutedStyle.Render(cmd)))
	}
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("press 1-9 to run • esc to close"))
	return panelStyle.Render(b.String())
}

// handleTaskKey interprets a key while the task picker is open: a digit runs
// the matching task, everything else closes the picker.
func (m *model) handleTaskKey(key string) tea.Cmd {
	m.showTasks = false
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return nil
	}
	idx := int(key[0] - '1')
	names := m.currentTaskNames()
	if idx >= len(names) {
		return nil
	}
	p := m.current()
	name := names[idx]
	m.statusText = "Running task " + name
	go func() {
		if err := p.RunTask(name, m.notify); err != nil {
			p.appendLog("[stacker] " + err.Error())
			m.notify()
		}
	}()
	return nil
}

func (m *model) processList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Processes"))
	b.WriteString("\n")
	for i, p := range m.processes {
		processStatus := p.Status()
		status := oneShotStatusLabel(p, processStatus)
		statusRendered := status
		// Error badge: detected error lines turn the status orange with a "!"
		// even while running; a real failed status stays red.
		if errs := p.Errors(); errs > 0 && processStatus != StatusFailed {
			status += "!"
			statusRendered = errorBadgeStyle.Render(status)
		} else if processStatus == StatusRunning {
			statusRendered = runningStyle.Render(status)
		} else if processStatus == StatusFailed {
			statusRendered = failedStyle.Render(status)
		}
		marker := ""
		markerWidth := 0
		if p.oneShot {
			marker = "▶ "
			markerWidth = 2
		}
		dot := ""
		dotWidth := 0
		if c := p.Color(); c != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("●") + " "
			dotWidth = 2
		}
		name := sanitizeLogLine(p.Name)
		if p.orphaned {
			name = name + " ⚠"
		}
		contentWidth := max(1, m.leftWidth()-5)
		nameWidth := max(1, contentWidth-len(status)-1-dotWidth-markerWidth)
		name = truncate(name, nameWidth)
		line := marker + dot + name + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + " " + statusRendered
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

// oneShotStatusLabel maps a stopped one-shot to "idle" so a finished run does
// not read like a crashed service; everything else keeps its status string.
func oneShotStatusLabel(p *Process, status ProcessStatus) string {
	if p.oneShot && status == StatusStopped {
		return "idle"
	}
	return string(status)
}

func (m *model) logView() string {
	p := m.current()
	if p == nil {
		return "No processes configured"
	}
	vis := m.visualLines(p.Logs(), m.logWidth())
	visibleLines := m.visibleLogLines()
	maxOffset := max(0, len(vis)-visibleLines)
	m.logOffset = clamp(m.logOffset, 0, maxOffset)
	end := min(len(vis), m.logOffset+visibleLines)

	var b strings.Builder
	title := fmt.Sprintf("Logs: %s [%s]", sanitizeLogLine(p.Name), p.Status())
	if errs := p.Errors(); errs > 0 {
		title += fmt.Sprintf(" — %d error line(s); space clears", errs)
	}
	if m.wrap {
		title += " — wrap"
	}
	if !m.follow {
		title += " — paused; press G for bottom"
	}
	b.WriteString(titleStyle.Render(truncate(title, m.logWidth())))
	b.WriteByte('\n')
	for i := m.logOffset; i < end; i++ {
		line := vis[i].text
		if m.isSelected(vis[i].idx) {
			line = selectionStyle.Render(line)
		}
		b.WriteString(line)
		if i+1 < end {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// visLine is one rendered log row: with wrap on, a long log line becomes
// several rows that all share the same logical index (used by selection).
type visLine struct {
	text string
	idx  int
}

func (m *model) visualLines(logs []string, width int) []visLine {
	out := make([]visLine, 0, len(logs))
	for i, line := range logs {
		if !m.wrap || ansi.StringWidth(line) <= width {
			out = append(out, visLine{text: truncate(line, width), idx: i})
			continue
		}
		for _, part := range strings.Split(ansi.Hardwrap(line, width, true), "\n") {
			out = append(out, visLine{text: part, idx: i})
		}
	}
	return out
}

func (m *model) logWidth() int { return max(10, m.width-m.leftWidth()-6) }

// totalVisualLines is the scrollable row count for the selected process.
func (m *model) totalVisualLines() int {
	p := m.current()
	if p == nil {
		return 0
	}
	return len(m.visualLines(p.Logs(), m.logWidth()))
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

// mouseLogLine maps a screen row to the logical log index under it, going
// through the wrapped rows so selection works with word wrap on.
func (m *model) mouseLogLine(y int) int {
	row := max(0, m.logOffset+(y-2))
	p := m.current()
	if p == nil {
		return row
	}
	vis := m.visualLines(p.Logs(), m.logWidth())
	if len(vis) == 0 {
		return 0
	}
	return vis[min(row, len(vis)-1)].idx
}

func (m *model) scrollLogs(delta int) {
	if m.current() == nil {
		return
	}
	maxOffset := max(0, m.totalVisualLines()-m.visibleLogLines())
	m.logOffset = clamp(m.logOffset+delta, 0, maxOffset)
	m.follow = m.logOffset >= maxOffset
}

func (m *model) scrollToBottom() {
	if m.current() == nil {
		return
	}
	m.logOffset = max(0, m.totalVisualLines()-m.visibleLogLines())
}

func (m *model) resetLogView() {
	m.clearSelection()
	m.follow = true
	m.scrollToBottom()
}

// markAllRunning appends the separator to every running process's logs.
func (m *model) markAllRunning() int {
	n := 0
	for _, p := range m.procs() {
		if p.Status() == StatusRunning {
			p.Mark()
			n++
		}
	}
	if n > 0 {
		m.notify()
	}
	return n
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

// requestOrder is called by the web server: the reorder is applied by the
// TUI goroutine on the next refresh, since it owns the selection index.
func (m *model) requestOrder(names []string) {
	m.pendingMu.Lock()
	m.pendingOrder = names
	m.pendingMu.Unlock()
	m.notify()
}

// applyPendingOrder runs on the TUI goroutine (refreshMsg).
func (m *model) applyPendingOrder() {
	m.pendingMu.Lock()
	names := m.pendingOrder
	m.pendingOrder = nil
	m.pendingMu.Unlock()
	if names == nil {
		return
	}
	// Only the process prefix is reorderable; one-shot tasks stay pinned
	// after it. names must be a permutation of the process names.
	byName := make(map[string]*Process, m.numProcesses)
	for _, p := range m.processes[:m.numProcesses] {
		byName[p.Name] = p
	}
	reordered := make([]*Process, 0, m.numProcesses)
	for _, name := range names {
		if p := byName[name]; p != nil {
			reordered = append(reordered, p)
			delete(byName, name)
		}
	}
	if len(reordered) != m.numProcesses {
		return
	}
	reordered = append(reordered, m.processes[m.numProcesses:]...)
	selectedName := ""
	if p := m.current(); p != nil {
		selectedName = p.Name
	}
	m.procsMu.Lock()
	m.processes = reordered
	m.procsMu.Unlock()
	for i, p := range reordered {
		if p.Name == selectedName {
			m.selected = i
			break
		}
	}
}

// moveSelectedCmd moves the selected process up/down in the list and
// persists the new order to the YAML config.
func (m *model) moveSelectedCmd(delta int) tea.Cmd {
	i := m.selected
	j := i + delta
	// One-shot tasks are pinned; reorder only within the process prefix.
	if i < 0 || i >= m.numProcesses || j < 0 || j >= m.numProcesses {
		if i >= m.numProcesses {
			m.statusText = "Tasks can't be reordered"
		}
		return nil
	}
	reordered := make([]*Process, len(m.processes))
	copy(reordered, m.processes)
	reordered[i], reordered[j] = reordered[j], reordered[i]
	m.procsMu.Lock()
	m.processes = reordered
	m.procsMu.Unlock()
	m.selected = j
	names := make([]string, m.numProcesses)
	for k := 0; k < m.numProcesses; k++ {
		names[k] = reordered[k].Name
	}
	path := m.configPath
	return func() tea.Msg {
		return orderSavedMsg{err: updateConfigOrder(path, names)}
	}
}

// colorPresets is the palette the TUI `c` key cycles through and the web
// selector offers as swatches. "" means no dot.
var colorPresets = []string{"", "#38bdf8", "#3b82f6", "#8b5cf6", "#d946ef", "#ec4899", "#ef4444", "#f97316", "#f59e0b", "#84cc16", "#22c55e", "#14b8a6"}

// nextPresetColor returns the palette entry after the given color, wrapping.
func nextPresetColor(current string) string {
	for i, c := range colorPresets {
		if c == current {
			return colorPresets[(i+1)%len(colorPresets)]
		}
	}
	return colorPresets[1]
}

// cycleColorCmd moves the selected process to the next preset color and
// persists it to the YAML config.
func (m *model) cycleColorCmd() tea.Cmd {
	p := m.current()
	if p == nil {
		return nil
	}
	// Color persistence writes into the processes: mapping; a one-shot task
	// lives under tasks:, so only cycle it in memory.
	if p.oneShot {
		next := nextPresetColor(p.Color())
		p.SetColor(next)
		m.statusText = "Color set (task color not saved)"
		return nil
	}
	next := nextPresetColor(p.Color())
	p.SetColor(next)
	path := m.configPath
	return func() tea.Msg {
		return colorSavedMsg{name: p.Name, color: next, err: updateConfigColor(path, p.Name, next)}
	}
}

func (m *model) openWebCmd(target string) tea.Cmd {
	return func() tea.Msg {
		// Copy the URL so it can be pasted even if no browser opens (e.g. SSH).
		_ = copyToClipboard(target)
		if !canOpenBrowser() {
			return webMsg{url: target, skippedBrowser: true}
		}
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
	for _, p := range m.procs() {
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
	if cfg.UI.WebPort < 0 || cfg.UI.WebPort > 65535 {
		return Config{}, errors.New("ui.web_port must be between 0 and 65535 (0 = default 52911)")
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
		if processCfg.Color != "" && !validColor(processCfg.Color) {
			return Config{}, fmt.Errorf("process %q has invalid color %q; use hex (#0af, #00aaff) or a CSS color name", name, processCfg.Color)
		}
		for taskName, taskCmd := range processCfg.Tasks {
			if strings.TrimSpace(taskName) == "" {
				return Config{}, fmt.Errorf("process %q has a task with an empty name", name)
			}
			if strings.TrimSpace(taskCmd) == "" {
				return Config{}, fmt.Errorf("process %q task %q has an empty command", name, taskName)
			}
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

	// Standalone one-shot tasks (root-level `tasks:`).
	for name, taskCfg := range cfg.Tasks {
		if strings.TrimSpace(name) == "" {
			return Config{}, errors.New("task name cannot be empty")
		}
		if _, clash := cfg.Processes[name]; clash {
			return Config{}, fmt.Errorf("task %q clashes with a process of the same name", name)
		}
		if strings.TrimSpace(taskCfg.Command) == "" {
			return Config{}, fmt.Errorf("task %q command cannot be empty", name)
		}
		if taskCfg.Color != "" && !validColor(taskCfg.Color) {
			return Config{}, fmt.Errorf("task %q has invalid color %q; use hex (#0af, #00aaff) or a CSS color name", name, taskCfg.Color)
		}
		if taskCfg.Cwd == "" {
			taskCfg.Cwd = "."
		}
		if !filepath.IsAbs(taskCfg.Cwd) {
			taskCfg.Cwd = filepath.Join(configDir, taskCfg.Cwd)
		}
		taskCfg.Cwd = filepath.Clean(taskCfg.Cwd)
		info, err := os.Stat(taskCfg.Cwd)
		if err != nil {
			return Config{}, fmt.Errorf("task %q cwd: %w", name, err)
		}
		if !info.IsDir() {
			return Config{}, fmt.Errorf("task %q cwd %q is not a directory", name, taskCfg.Cwd)
		}
		cfg.Tasks[name] = taskCfg
	}

	// Keep the YAML key order: it is the display order everywhere.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil && len(doc.Content) > 0 {
		if processes := mappingValue(doc.Content[0], "processes"); processes != nil {
			for i := 0; i+1 < len(processes.Content); i += 2 {
				cfg.processOrder = append(cfg.processOrder, processes.Content[i].Value)
			}
		}
		if tasks := mappingValue(doc.Content[0], "tasks"); tasks != nil {
			for i := 0; i+1 < len(tasks.Content); i += 2 {
				cfg.taskOrder = append(cfg.taskOrder, tasks.Content[i].Value)
			}
		}
	}
	return cfg, nil
}

func main() {
	configPath, rest := parseArgs(os.Args[1:])
	if len(rest) > 0 {
		os.Exit(runCLI(configPath, rest))
	}

	// Bare `stacker` with a running serve instance attaches the TUI.
	// If the default stacker.yml is not running but exactly one serve is up
	// (e.g. --config codebunker.yml serve -d), attach to that one.
	if st, used, fallback, err := resolveRunningInstance(configPath); err == nil && st != nil && st.Mode == "serve" {
		if fallback {
			fmt.Fprintf(os.Stderr, "note: attaching to only running config %s\n", used)
		}
		os.Exit(runAttach(used))
	}

	os.Exit(runSession(configPath))
}

// runSession starts supervisor + TUI in one process (default interactive mode).
// q / ctrl+c stops every process and exits.
func runSession(configPath string) int {
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	m := newModel(cfg)
	m.mode = "session"
	control, err := startControlServer(m, configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control plane error:", err)
		return 1
	}
	defer control.Close()
	m.configPath = control.config
	m.startConfigWatcher()
	defer m.stopConfigWatcher()

	// /v1/down from CLI cancels the TUI context path via shutdown.
	go func() {
		select {
		case <-m.shutdown:
			stopSignals()
		case <-ctx.Done():
		}
	}()

	program := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
		tea.WithContext(ctx),
	)
	_, runErr := program.Run()
	m.stopWeb()
	m.stopAll()
	if runErr != nil && !(ctx.Err() != nil && errors.Is(runErr, tea.ErrProgramKilled)) {
		fmt.Fprintln(os.Stderr, "stacker error:", runErr)
		return 1
	}
	return 0
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
		case a == "-v" || a == "--version" || a == "-version":
			rest = append(rest, "version")
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

// validColor accepts #rgb/#rrggbb hex or a plain CSS color name. The value is
// rendered by both lipgloss and the browser, so keep the grammar tight.
func validColor(c string) bool {
	if strings.HasPrefix(c, "#") {
		hex := c[1:]
		if len(hex) != 3 && len(hex) != 6 {
			return false
		}
		for _, r := range hex {
			if !unicode.Is(unicode.ASCII_Hex_Digit, r) {
				return false
			}
		}
		return true
	}
	for _, r := range c {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			return false
		}
	}
	return len(c) > 0
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
