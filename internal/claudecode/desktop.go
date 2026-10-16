package claudecode

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A master in Claude Desktop (harness.DesktopApp): Desktop takes no
// command line, it relaunches a session with its own options, so the
// master's brief, the tools it may not use and the acw commands it may
// run reach it through the project's settings instead, hooks that answer
// for its session only.

// BoardLaunch is the name of the board's entry in .claude/launch.json,
// which Desktop's Browser pane opens.
const BoardLaunch = "acw-board"

// Desktop is what acw installed for a master in Claude Desktop, kept in
// the status dir so that acw stop takes off exactly that.
type Desktop struct {
	// Dir is the master's folder: Desktop reads settings and launch
	// configurations from its .claude.
	Dir     string `json:"dir"`
	Session string `json:"session"`
	// Hook is the command the hooks run, before their event name.
	Hook string `json:"hook"`
	// Allow are the permission rules acw added, not those already there.
	Allow []string `json:"allow"`
	// Board is the board's URL, "" when acw declared no launch for it.
	Board string `json:"board,omitempty"`
	// InboxNext is what the master runs in the background to get its
	// messages, said again at every start: Desktop drops it on a restart.
	InboxNext string `json:"inbox_next"`
}

func desktopFile(statusDir string) string { return filepath.Join(statusDir, "desktop.json") }

// IsDesktop reports that the swarm whose status dir this is has its
// master in Claude Desktop.
func IsDesktop(statusDir string) bool {
	_, err := os.Stat(desktopFile(statusDir))
	return err == nil
}

// kickoffFile holds what the master reads once, at its first start.
func kickoffFile(statusDir string) string { return filepath.Join(statusDir, "master.kickoff") }

// BriefFile is the master's brief, which it reads at every start.
func BriefFile(statusDir string) string { return filepath.Join(statusDir, "master.system.md") }

// NewSessionID is a session id for claude --session-id, a random UUID.
func NewSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CreateSession makes the master's session in dir, under id: one short
// turn, so that Desktop has a session to resume. Its brief comes later,
// from the SessionStart hook.
func CreateSession(dir, id, name, model string) error {
	args := []string{"-p", "--session-id", id, "-n", name}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "acw created this session for the master of its swarm; its brief comes when it starts in Claude Desktop. Reply OK.")
	cmd := exec.Command("claude", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude -p: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OpenDesktop opens the session in Claude Desktop. It needs the user's
// terminal: claude refuses --desktop without one.
func OpenDesktop(dir, id string) error {
	cmd := exec.Command("claude", "--desktop", "--resume", id)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// WakeSession starts the first turn of the session named name, id, once
// Claude Desktop runs it: a session only takes a turn from a message, and
// nothing outside a Claude session can send one, so a one-shot haiku
// session carries it, with SendMessage. What it says hardly matters: the
// SessionStart hook already handed the master its brief and kickoff.
func WakeSession(dir, id, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for !running(id) {
		if time.Now().After(deadline) {
			return fmt.Errorf("session %s not started by Claude Desktop within %s", id, timeout)
		}
		time.Sleep(2 * time.Second)
	}
	prompt := fmt.Sprintf("Use your SendMessage tool, once, to send this exact text to the session named %q: \"acw: your swarm is up, start as your brief says.\" Do nothing else, and reply SENT.", name)
	cmd := exec.Command("claude", "-p", "--model", "haiku", "--allowedTools", "SendMessage", "ListAgents", "--setting-sources", "", "--", prompt)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude -p: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// running reports that session id runs, as claude agents lists it.
func running(id string) bool {
	out, err := exec.Command("claude", "agents", "--json").Output()
	if err != nil {
		return false
	}
	var sessions []struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(out, &sessions) != nil {
		return false
	}
	return slices.ContainsFunc(sessions, func(s struct {
		SessionID string `json:"sessionId"`
	}) bool {
		return s.SessionID == id
	})
}

// InstallDesktop writes d's hooks and permission rules into d.Dir's
// .claude/settings.local.json and, with a board, its launch configuration
// into .claude/launch.json, next to what is there; kickoff is what the
// master reads at its first start. allow are the rules the master needs:
// those already there are left out of d.Allow, so acw stop keeps them.
func InstallDesktop(statusDir string, d Desktop, allow []string, kickoff string) error {
	if err := os.WriteFile(kickoffFile(statusDir), []byte(kickoff), 0o600); err != nil {
		return err
	}
	err := editJSON(filepath.Join(d.Dir, ".claude", "settings.local.json"), func(m map[string]any) {
		addHook(m, "SessionStart", "", d.Hook+" session-start")
		addHook(m, "PreToolUse", "Edit|Write|MultiEdit|NotebookEdit|EnterPlanMode|ExitPlanMode", d.Hook+" pre-tool")
		perms := object(m, "permissions")
		have := strings_(perms["allow"])
		for _, rule := range allow {
			if !slices.Contains(have, rule) {
				have = append(have, rule)
				d.Allow = append(d.Allow, rule)
			}
		}
		perms["allow"] = have
	})
	if err != nil {
		return err
	}
	if d.Board != "" {
		port, err := urlPort(d.Board)
		if err != nil {
			return err
		}
		err = editJSON(filepath.Join(d.Dir, ".claude", "launch.json"), func(m map[string]any) {
			if _, ok := m["version"]; !ok {
				m["version"] = "0.0.1"
			}
			configs := withoutBoard(m["configurations"])
			m["configurations"] = append(configs, map[string]any{"name": BoardLaunch, "url": d.Board, "port": port})
		})
		if err != nil {
			return err
		}
	}
	return writeDesktop(statusDir, d)
}

// UninstallDesktop takes off what InstallDesktop wrote, and nothing else.
// A swarm with no master in Desktop has nothing to take off.
func UninstallDesktop(statusDir string) error {
	content, err := os.ReadFile(desktopFile(statusDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var d Desktop
	if err := json.Unmarshal(content, &d); err != nil {
		return err
	}
	err = editJSON(filepath.Join(d.Dir, ".claude", "settings.local.json"), func(m map[string]any) {
		hooks := object(m, "hooks")
		for event, entries := range hooks {
			kept := withoutCommand(entries, d.Hook)
			if len(kept) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = kept
			}
		}
		if len(hooks) == 0 {
			delete(m, "hooks")
		}
		perms := object(m, "permissions")
		var kept []any
		for _, rule := range strings_(perms["allow"]) {
			if !slices.Contains(d.Allow, rule) {
				kept = append(kept, rule)
			}
		}
		if len(kept) == 0 {
			delete(perms, "allow")
		} else {
			perms["allow"] = kept
		}
		if len(perms) == 0 {
			delete(m, "permissions")
		}
	})
	if err != nil {
		return err
	}
	if d.Board == "" {
		return nil
	}
	return editJSON(filepath.Join(d.Dir, ".claude", "launch.json"), func(m map[string]any) {
		configs := withoutBoard(m["configurations"])
		if len(configs) == 0 {
			clear(m)
			return
		}
		m["configurations"] = configs
	})
}

// DesktopHook answers the hooks InstallDesktop installed, for the
// master's session only: every other session of the folder (the user's
// own, a worker's) gets nothing from them.
func DesktopHook(event, statusDir string, in io.Reader, out io.Writer) error {
	var input struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
		toolUse
	}
	if err := json.NewDecoder(in).Decode(&input); err != nil {
		return err
	}
	content, err := os.ReadFile(desktopFile(statusDir))
	if err != nil {
		// No swarm any more: the hook was left behind, it answers nothing.
		return nil
	}
	var d Desktop
	if err := json.Unmarshal(content, &d); err != nil {
		return err
	}
	if input.SessionID != d.Session {
		return nil
	}
	switch event {
	case "pre-tool":
		return guard(BriefsDir(statusDir), input.toolUse, out)
	case "session-start":
		return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": startContext(statusDir, d, input.Source),
		}})
	}
	return fmt.Errorf("unknown hook event %q", event)
}

// startContext is what the master reads at each start of its session:
// where its brief is, since Desktop takes no system prompt; its inbox
// watch to arm again, since Desktop drops background commands; the board
// to open; and, once, the kickoff.
func startContext(statusDir string, d Desktop, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the master of an acw swarm, running in Claude Desktop. Your brief is the file %s: read it in full with your Read tool now, before anything else, and follow it; read it again after every compaction. It stands for the system prompt Claude Desktop can't take.", BriefFile(statusDir))
	b.WriteString(" One correction to it: you are in no Herdr pane, and your workers run as Claude Code background sessions, not Herdr panes. Whatever your brief says about herdr (agent read, agent prompt, agent send-keys, agent wait) does not apply to them: never run a herdr command. A worker's status file and acw status are what you read; acw tell reaches it as before. A worker waiting on a permission prompt is the user's to answer, in a terminal, with `claude attach <its name>`: tell them so.")
	if d.InboxNext != "" {
		fmt.Fprintf(&b, " Claude Desktop drops your background commands whenever it restarts this session: run `%s` in the background again now, as your brief says.", d.InboxNext)
	}
	if d.Board != "" && source != "compact" {
		fmt.Fprintf(&b, " Then open acw board for the user, in the Browser pane: your preview tool, on the launch configuration %s (%s).", BoardLaunch, d.Board)
	}
	if kickoff, err := os.ReadFile(kickoffFile(statusDir)); err == nil {
		b.WriteString("\n\n" + string(kickoff))
		_ = os.Remove(kickoffFile(statusDir))
	}
	return b.String()
}

func writeDesktop(statusDir string, d Desktop) error {
	content, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(desktopFile(statusDir), content, 0o600)
}

// editJSON applies edit to the JSON object in path, made when absent, and
// writes it back in one rename; an object left empty removes the file.
func editJSON(path string, edit func(map[string]any)) error {
	m := map[string]any{}
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(content, &m); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	edit(m)
	if len(m) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".acw-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(content, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// object is m[key] as an object, made when absent.
func object(m map[string]any, key string) map[string]any {
	o, ok := m[key].(map[string]any)
	if !ok {
		o = map[string]any{}
		m[key] = o
	}
	return o
}

// strings_ is v as a list of strings, its other items dropped.
func strings_(v any) []string {
	var list []string
	items, _ := v.([]any)
	for _, item := range items {
		if s, ok := item.(string); ok {
			list = append(list, s)
		}
	}
	return list
}

// addHook appends to m's hooks for event an entry running command,
// unless one already does.
func addHook(m map[string]any, event, matcher, command string) {
	hooks := object(m, "hooks")
	entries, _ := hooks[event].([]any)
	if len(withoutCommand(entries, command)) < len(entries) {
		return
	}
	entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	hooks[event] = append(entries, entry)
}

// withoutCommand drops the hook entries whose commands start with prefix.
func withoutCommand(v any, prefix string) []any {
	var kept []any
	entries, _ := v.([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		ours := false
		for _, h := range inner {
			hook, _ := h.(map[string]any)
			if c, _ := hook["command"].(string); strings.HasPrefix(c, prefix) {
				ours = true
			}
		}
		if !ours {
			kept = append(kept, e)
		}
	}
	return kept
}

// withoutBoard drops acw's board from the launch configurations.
func withoutBoard(v any) []any {
	var kept []any
	configs, _ := v.([]any)
	for _, c := range configs {
		if config, _ := c.(map[string]any); config["name"] != BoardLaunch {
			kept = append(kept, c)
		}
	}
	return kept
}

func urlPort(raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0, fmt.Errorf("board url %s: no port", raw)
	}
	return port, nil
}
