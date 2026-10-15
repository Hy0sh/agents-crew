package claudecode

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Workers as Claude Code background sessions (claude --bg), the engine a
// master in Claude Desktop implies (see harness.DesktopApp). Nothing can
// type into a running session from outside it, so acw never does: a task
// starts a new session with its brief as first prompt, and a message to a
// worker stops its session and resumes it with the message, under the
// same id and its saved options. Spiked on 15/10 (Claude Code 2.1.300).

// Session is one row of claude agents --json.
type Session struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	Cwd        string `json:"cwd"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
}

// Sessions lists the running sessions, background and interactive.
func Sessions() ([]Session, error) {
	out, err := exec.Command("claude", "agents", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	var sessions []Session
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	return sessions, nil
}

// FindSession is the running session named name.
func FindSession(sessions []Session, name string) (Session, bool) {
	for _, s := range sessions {
		if s.Name == name {
			return s, true
		}
	}
	return Session{}, false
}

// AgentStatus is s in herdr's words, which the rest of acw reads: busy is
// working, waiting (on a permission prompt) is blocked, idle stays idle.
func (s Session) AgentStatus() string {
	switch s.Status {
	case "busy":
		return "working"
	case "waiting":
		return "blocked"
	case "idle":
		return "idle"
	}
	return "unknown"
}

// StartBackground starts a background session named name in dir, with
// args (model, system prompt, settings, tools), on prompt; with no prompt
// it waits for one, idle.
func StartBackground(dir, name string, args []string, prompt string) error {
	cmdArgs := append([]string{"--bg", "-n", name}, args...)
	if prompt != "" {
		cmdArgs = append(cmdArgs, "--", prompt)
	}
	cmd := exec.Command("claude", cmdArgs...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude --bg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopSession stops s and returns once it is gone from the list: resumed
// before, it would start a copy instead.
func StopSession(s Session, timeout time.Duration) error {
	if out, err := exec.Command("claude", "stop", s.ID).CombinedOutput(); err != nil {
		return fmt.Errorf("claude stop %s: %w: %s", s.ID, err, strings.TrimSpace(string(out)))
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		sessions, err := Sessions()
		if err != nil {
			return err
		}
		if _, ok := FindSession(sessions, s.Name); !ok {
			return nil
		}
	}
	return fmt.Errorf("%s still running %s after claude stop", s.Name, timeout)
}

// StopAll stops every running session whose name ours accepts, for acw
// stop: background sessions outlive the terminal that started them.
func StopAll(ours func(name string) bool, timeout time.Duration) {
	sessions, err := Sessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s.Kind == "background" && ours(s.Name) {
			if err := StopSession(s, timeout); err != nil {
				fmt.Printf("%s: %v\n", s.Name, err)
			}
		}
	}
}

// ResumeWith resumes the stopped session s, in the background, on prompt:
// same id, same saved options. Its full id, never the short one, and no
// option: either makes a copy instead.
func ResumeWith(s Session, prompt string) error {
	cmd := exec.Command("claude", "--resume", s.SessionID, "--bg", prompt)
	cmd.Dir = s.Cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude --resume %s --bg: %w: %s", s.SessionID, err, strings.TrimSpace(string(out)))
	}
	return nil
}
