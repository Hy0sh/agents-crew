// Package config reads acw's per-project settings: a personal file,
// outside any repo, keyed by the directory acw is launched from.
//
// It is an override, not a registry: acw works on any repo with no entry
// at all, and an entry only changes the defaults of the flags it names
// (a flag given on the command line still wins). Outside the repo so it
// works on repos where nothing may be committed, e.g. a client's.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/Hy0sh/agents-crew/internal/names"
)

// Project is one repo's entry. Keys are the flag names, so there is
// nothing to map in one's head. Pointers because absent and "" differ:
// `"worker-model": ""` means "pass no --model", like the flag. omitempty
// so that Edit writes back only the keys set: it drops nil, keeps "".
type Project struct {
	Workers *int `json:"workers,omitempty"`
	// MinWorkers is how many workers stay open with nothing queued.
	MinWorkers  *int    `json:"min-workers,omitempty"`
	MaxStacks   *int    `json:"max-stacks,omitempty"`
	MasterKind  *string `json:"master-kind,omitempty"`
	WorkerKind  *string `json:"worker-kind,omitempty"`
	MasterModel *string `json:"master-model,omitempty"`
	WorkerModel *string `json:"worker-model,omitempty"`
	// MasterAutocompact is the context size, in tokens, at which a claude
	// master compacts; 0 leaves Claude Code's own.
	MasterAutocompact *int    `json:"master-autocompact,omitempty"`
	Brief             *string `json:"brief,omitempty"`
	// BriefExtra is a template appended to the brief, built-in or custom:
	// a mode such as a test campaign, without forking the whole brief.
	BriefExtra *string `json:"brief-extra,omitempty"`
	Profile    *string `json:"profile,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	// MasterDir is where the master starts instead of the repo, e.g. a
	// folder whose .claude it needs. Checked by the caller.
	MasterDir *string `json:"master-dir,omitempty"`
	// SilenceMinutes is how long a working worker may show no activity at
	// all before acw's watcher tells the master.
	SilenceMinutes *int `json:"silence-minutes,omitempty"`
	// IdleCloseMinutes is how long a free worker stays open, above
	// min-workers, with nothing queued for it.
	IdleCloseMinutes *int `json:"idle-close-minutes,omitempty"`
	// PRWatch makes acw's watcher follow the user's open pull requests on
	// the repo and tell the master what changed on them.
	PRWatch *bool `json:"pr-watch,omitempty"`
	// Roles are the kinds of worker of the swarm, in the order written:
	// what each runs, which tasks it takes, how many may be open. Without
	// them, workers and min-workers make one role, worker.
	Roles Roles `json:"roles,omitempty"`
	// WorkerOverrides is only read to be refused with the roles that
	// replace it (see CheckRoles).
	WorkerOverrides map[string]WorkerOverride `json:"worker-overrides,omitempty"`
	// Presets are named variants of the entry, picked with --preset: same
	// keys, laid over it by WithPreset. A preset holding presets of its own
	// is refused by Load.
	Presets map[string]Project `json:"presets,omitempty"`
}

// Role is one kind of worker of a swarm: what its instances run, which
// tasks they take, and how many may be open (Max) or are kept open (Min).
// An absent key takes the entry's own (worker-kind, worker-model,
// profile). Same absent-vs-"" rule as Project.
type Role struct {
	Max    *int    `json:"max,omitempty"`
	Min    *int    `json:"min,omitempty"`
	Kind   *string `json:"kind,omitempty"`
	Model  *string `json:"model,omitempty"`
	Prompt *string `json:"prompt,omitempty"`
	// Dir makes its instances workers outside the code: they start there,
	// with no worktree, environment or branch. Checked by the caller.
	Dir *string `json:"dir,omitempty"`
	// Profile is the wtm stack profile its instances start on instead of
	// the swarm's.
	Profile *string `json:"profile,omitempty"`
	// Tasks are the kinds of task its instances take (acw queue add
	// --kind); a role without any takes the tasks with no kind.
	Tasks []string `json:"tasks,omitempty"`
}

// NamedRole is a role and its name, the prefix of its instances' names.
type NamedRole struct {
	Name string
	Role
}

// Roles keeps the order they are written in: the first role with room
// is the one the pool opens a worker of (see schedule in cmd/acw).
type Roles []NamedRole

func (r *Roles) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("roles: an object of roles, by name")
	}
	*r = Roles{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		var role Role
		inner := json.NewDecoder(bytes.NewReader(raw))
		inner.DisallowUnknownFields()
		if err := inner.Decode(&role); err != nil {
			return fmt.Errorf("roles: %s: %w", name, err)
		}
		*r = append(*r, NamedRole{Name: name, Role: role})
	}
	_, err := dec.Token()
	return err
}

func (r Roles) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, nr := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(nr.Name)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(nr.Role)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// CheckRoles refuses what roles can't mean, and worker-overrides, which
// they replace, with the roles to write instead.
func (p *Project) CheckRoles() error {
	if len(p.WorkerOverrides) > 0 {
		return fmt.Errorf("worker-overrides is replaced by roles; write instead (rename the roles as you like):\n%s", overridesAsRoles(p))
	}
	if len(p.Roles) == 0 {
		return nil
	}
	if p.Workers != nil || p.MinWorkers != nil {
		return errors.New("roles sets how many workers run: drop workers and min-workers")
	}
	seen := map[string]bool{}
	for _, r := range p.Roles {
		switch {
		case !names.ValidRole(r.Name):
			return fmt.Errorf("roles: %q is not a role name (lowercase letters and -, at most 20, not ending with a digit, not master)", r.Name)
		case seen[r.Name]:
			return fmt.Errorf("roles: %s twice", r.Name)
		case r.Max == nil || *r.Max < 1:
			return fmt.Errorf("roles: %s: max, how many may be open, is required and at least 1", r.Name)
		case r.Min != nil && (*r.Min < 0 || *r.Min > *r.Max):
			return fmt.Errorf("roles: %s: min is from 0 to max (%d)", r.Name, *r.Max)
		case r.Dir != nil && r.Profile != nil:
			return fmt.Errorf("roles: %s: a role with dir has no stack, so no profile", r.Name)
		}
		seen[r.Name] = true
	}
	return nil
}

// overridesAsRoles is what an entry with worker-overrides reads as in
// roles: the workers it didn't override as role worker, each override a
// role of one, kept open if it was kept.
func overridesAsRoles(p *Project) string {
	one := 1
	var roles Roles
	if p.Workers != nil {
		rest := *p.Workers - len(p.WorkerOverrides)
		if rest > 0 {
			roles = append(roles, NamedRole{Name: "worker", Role: Role{Max: &rest, Min: p.MinWorkers}})
		}
	}
	used := map[string]bool{"worker": true}
	for i, key := range slices.Sorted(maps.Keys(p.WorkerOverrides)) {
		o := p.WorkerOverrides[key]
		r := Role{Max: &one, Kind: o.Kind, Model: o.Model, Prompt: o.Prompt, Dir: o.Dir, Profile: o.Profile, Tasks: o.Tasks}
		if o.Keep != nil && *o.Keep {
			r.Min = &one
		}
		// Named after what it takes when that makes a role name, else by
		// letter: a role name can't end with a digit.
		name := "special-" + string(rune('a'+i%26))
		if len(o.Tasks) > 0 && names.ValidRole(o.Tasks[0]) && !used[o.Tasks[0]] {
			name = o.Tasks[0]
		}
		used[name] = true
		roles = append(roles, NamedRole{Name: name, Role: r})
	}
	content, _ := json.MarshalIndent(struct {
		Roles Roles `json:"roles"`
	}{roles}, "", "  ")
	return string(content)
}

// WorkerOverride is the former per-index setting, read only to be turned
// into roles. Same absent-vs-"" rule as Project.
type WorkerOverride struct {
	Kind   *string `json:"kind,omitempty"`
	Model  *string `json:"model,omitempty"`
	Prompt *string `json:"prompt,omitempty"`
	// Dir makes the worker one outside the code: it starts there, with no
	// worktree, environment or branch. Checked by the caller.
	Dir *string `json:"dir,omitempty"`
	// Tasks are the kinds of task the worker takes (acw queue add --kind):
	// a task of a kind goes only to the workers that list it.
	Tasks []string `json:"tasks,omitempty"`
	// Keep opens the worker with the swarm and never closes it for being
	// idle: a reviewer is there when the first review comes.
	Keep *bool `json:"keep,omitempty"`
	// Profile is the wtm stack profile this worker starts on instead of
	// the swarm's: a reviewer of backend changes needs no frontend.
	Profile *string `json:"profile,omitempty"`
}

type file struct {
	Projects map[string]Project `json:"projects"`
}

// Path is where the config lives: $XDG_CONFIG_HOME/acw/config.json, else
// ~/.config/acw/config.json. Not os.UserConfigDir, which is
// ~/Library/Application Support on macOS — not where a wtm user looks.
func Path() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "acw", "config.json")
	}
	return filepath.Join(expandHome("~"), ".config", "acw", "config.json")
}

// Load returns the entry for repo, or nil when there is no config file or
// no entry for it. An unreadable file, invalid JSON or an unknown key is
// an error: a typo silently ignored is the worst outcome for a config.
func Load(repo string) (*Project, error) {
	path := Path()
	f, err := readFile()
	if err != nil {
		return nil, err
	}
	if key := f.keyOf(repo); key != "" {
		p := f.Projects[key]
		expandPaths(p)
		if err := p.CheckRoles(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for name, preset := range p.Presets {
			if len(preset.Presets) > 0 {
				return nil, fmt.Errorf("%s: preset %q: a preset can't contain presets", path, name)
			}
			expandPaths(preset)
		}
		return &p, nil
	}
	return nil, nil
}

// readFile reads the config as written, ~ unexpanded: an empty file when
// there is none. Invalid JSON or an unknown key is an error.
func readFile() (file, error) {
	path := Path()
	var f file
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// keyOf is the key of repo's entry, as written ("~/..." or not), "" when
// it has none.
func (f file) keyOf(repo string) string {
	repo = filepath.Clean(repo)
	for key := range f.Projects {
		if filepath.Clean(expandHome(key)) == repo {
			return key
		}
	}
	return ""
}

// Edit applies change to repo's entry, made first when there is none, and
// writes the config back, with every other entry as it was. The file is
// replaced in one rename: an interrupted write never leaves half a config.
func Edit(repo string, change func(*Project) error) error {
	f, err := readFile()
	if err != nil {
		return err
	}
	key := f.keyOf(repo)
	if key == "" {
		key = filepath.Clean(repo)
		if f.Projects == nil {
			f.Projects = map[string]Project{}
		}
	}
	p := f.Projects[key]
	if err := change(&p); err != nil {
		return err
	}
	// Never write what Load would refuse: every later acw start in the
	// repo would fail until the JSON is fixed by hand.
	if err := p.CheckRoles(); err != nil {
		return err
	}
	f.Projects[key] = p

	content, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// FilesDir is where acw project keeps the copies of repo's files (notes,
// brief, brief-extra), next to the config: the directory's name, so it is
// recognizable, and the repo's hash, so two repos named alike don't
// share it.
func FilesDir(repo, slug string) string {
	return filepath.Join(filepath.Dir(Path()), filepath.Base(repo)+"-"+slug)
}

// expandPaths expands ~ in p's file paths. p is a copy, but its fields
// are pointers, so the expansion lands in the caller's entry.
func expandPaths(p Project) {
	paths := []*string{p.Brief, p.BriefExtra, p.Notes, p.MasterDir}
	for _, r := range p.Roles {
		paths = append(paths, r.Prompt, r.Dir)
	}
	for _, s := range paths {
		if s != nil {
			*s = expandHome(*s)
		}
	}
}

// WithPreset returns the entry with the named preset laid over it. A key
// the preset sets replaces the entry's whole value, roles included:
// merged role by role, a preset would inherit roles written for another
// composition of the swarm. A preset's count of workers (roles, or
// workers and min-workers) replaces the entry's, whichever way it is
// written; the result is checked as a whole.
func (p *Project) WithPreset(name string) (*Project, error) {
	preset, ok := p.Presets[name]
	if !ok {
		available := "none is defined"
		if len(p.Presets) > 0 {
			available = "available: " + strings.Join(slices.Sorted(maps.Keys(p.Presets)), ", ")
		}
		return nil, fmt.Errorf("unknown preset %q (%s)", name, available)
	}
	merged := *p
	// The count of workers is one value written two ways: the preset's
	// replaces the entry's, whichever way each is written.
	if preset.Roles != nil {
		merged.Workers, merged.MinWorkers = nil, nil
	}
	if preset.Workers != nil || preset.MinWorkers != nil {
		merged.Roles = nil
	}
	dst, src := reflect.ValueOf(&merged).Elem(), reflect.ValueOf(preset)
	for i := range src.NumField() {
		// Every field is a pointer or a map: nil is exactly "not set".
		if f := src.Field(i); !f.IsNil() {
			dst.Field(i).Set(f)
		}
	}
	merged.Presets = nil
	if err := merged.CheckRoles(); err != nil {
		return nil, fmt.Errorf("preset %q: %w", name, err)
	}
	return &merged, nil
}

// Summary lists the keys the entry sets, for the launch line that tells
// the user where a value came from.
func (p *Project) Summary() string {
	var parts []string
	addInt := func(k string, v *int) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", k, *v))
		}
	}
	addStr := func(k string, v *string) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%q", k, *v))
		}
	}
	addInt("workers", p.Workers)
	addInt("min-workers", p.MinWorkers)
	addInt("max-stacks", p.MaxStacks)
	addStr("master-kind", p.MasterKind)
	addStr("worker-kind", p.WorkerKind)
	addStr("master-model", p.MasterModel)
	addStr("worker-model", p.WorkerModel)
	addInt("master-autocompact", p.MasterAutocompact)
	addStr("brief", p.Brief)
	addStr("brief-extra", p.BriefExtra)
	addStr("profile", p.Profile)
	addStr("notes", p.Notes)
	addStr("master-dir", p.MasterDir)
	addInt("silence-minutes", p.SilenceMinutes)
	addInt("idle-close-minutes", p.IdleCloseMinutes)
	if p.PRWatch != nil {
		parts = append(parts, fmt.Sprintf("pr-watch=%t", *p.PRWatch))
	}
	if len(p.Roles) > 0 {
		roles := make([]string, len(p.Roles))
		for i, r := range p.Roles {
			roles[i] = fmt.Sprintf("%s:%d", r.Name, *r.Max)
		}
		parts = append(parts, "roles="+strings.Join(roles, ","))
	}
	return strings.Join(parts, ", ")
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
