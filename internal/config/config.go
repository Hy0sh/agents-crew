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
	"os"
	"path/filepath"
	"strings"
)

// Project is one repo's entry. Keys are the flag names, so there is
// nothing to map in one's head. Pointers because absent and "" differ:
// `"worker-model": ""` means "pass no --model", like the flag.
type Project struct {
	Workers     *int    `json:"workers"`
	MaxStacks   *int    `json:"max-stacks"`
	MasterKind  *string `json:"master-kind"`
	WorkerKind  *string `json:"worker-kind"`
	MasterModel *string `json:"master-model"`
	WorkerModel *string `json:"worker-model"`
	Brief       *string `json:"brief"`
	Profile     *string `json:"profile"`
	Notes       *string `json:"notes"`
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
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	repo = filepath.Clean(repo)
	for key, p := range f.Projects {
		if filepath.Clean(expandHome(key)) != repo {
			continue
		}
		for _, s := range []*string{p.Brief, p.Notes} {
			if s != nil {
				*s = expandHome(*s)
			}
		}
		return &p, nil
	}
	return nil, nil
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
	addInt("max-stacks", p.MaxStacks)
	addStr("master-kind", p.MasterKind)
	addStr("worker-kind", p.WorkerKind)
	addStr("master-model", p.MasterModel)
	addStr("worker-model", p.WorkerModel)
	addStr("brief", p.Brief)
	addStr("profile", p.Profile)
	addStr("notes", p.Notes)
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
