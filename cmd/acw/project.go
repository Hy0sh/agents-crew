package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hy0sh/agents-crew/internal/config"
	"github.com/Hy0sh/agents-crew/internal/names"
)

// projectKey is a key of a project's config entry that acw project sets:
// its flag has the key's name. A file key's value is copied next to the
// config, so the entry no longer depends on where the file was.
type projectKey struct {
	name, usage string
	file        bool
}

// presets and roles are left to the JSON: nested, they don't
// fit a flag or a question.
var projectKeys = []projectKey{
	{name: "workers", usage: "most worker agents open at once"},
	{name: "min-workers", usage: "workers kept open with nothing queued"},
	{name: "idle-close-minutes", usage: "how long a free worker above min-workers stays open"},
	{name: "max-stacks", usage: "concurrent isolated environments the machine can hold"},
	{name: "master-kind", usage: "herdr agent kind for the master (claude, codex, gemini...)"},
	{name: "worker-kind", usage: "herdr agent kind for the workers"},
	{name: "master-model", usage: "model for the master; empty passes no --model"},
	{name: "worker-model", usage: "model for the workers; empty passes no --model"},
	{name: "master-autocompact", usage: "context size in tokens at which a claude master compacts; 0 leaves Claude Code's own"},
	{name: "pr-watch", usage: "follow your open pull requests and tell the master what changed"},
	{name: "profile", usage: "wtm stack profile workers start on"},
	{name: "silence-minutes", usage: "how long a working worker may show no activity before the master hears of it"},
	{name: "master-dir", usage: "absolute folder outside the repo the master starts in"},
	{name: "brief", usage: "custom master brief template, copied next to the config", file: true},
	{name: "brief-extra", usage: "template appended to the brief, copied next to the config", file: true},
	{name: "notes", usage: "the repo's hard rules, copied next to the config", file: true},
}

// field is the entry's pointer field for a key, found by its JSON name.
func field(p *config.Project, name string) reflect.Value {
	v := reflect.ValueOf(p).Elem()
	for i := range v.NumField() {
		tag, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		if tag == name {
			return v.Field(i)
		}
	}
	panic("acw project: no config key " + name)
}

// setKey parses raw for the key's type and stores it in p.
func setKey(p *config.Project, name, raw string) error {
	f := field(p, name)
	switch f.Type().Elem().Kind() {
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", name, raw)
		}
		f.Set(reflect.ValueOf(&n))
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s: %q is not true or false", name, raw)
		}
		f.Set(reflect.ValueOf(&b))
	default:
		f.Set(reflect.ValueOf(&raw))
	}
	return nil
}

// shown is a key's current value as the stepper shows it.
func shown(p *config.Project, name string) string {
	f := field(p, name)
	if f.IsNil() {
		return "unset"
	}
	if s, ok := f.Elem().Interface().(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(f.Elem().Interface())
}

// copyIn copies a file the user named into dir and returns the copy's
// path. One already in dir is kept where it is; one of the same name is
// replaced, which is what giving the path again means.
func copyIn(dir, src string) (string, error) {
	if src == "~" || strings.HasPrefix(src, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		src = filepath.Join(home, strings.TrimPrefix(src, "~"))
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	if filepath.Dir(src) == dir {
		return src, nil
	}
	content, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	return dst, os.WriteFile(dst, content, 0o644)
}

// setValue sets a key from what the user gave, copying a file key's file
// first. An empty file key removes it: there is no file to copy.
func setValue(p *config.Project, k projectKey, raw, filesDir string) error {
	if k.file {
		if raw == "" {
			field(p, k.name).SetZero()
			return nil
		}
		dst, err := copyIn(filesDir, raw)
		if err != nil {
			return fmt.Errorf("%s: %w", k.name, err)
		}
		raw = dst
	}
	return setKey(p, k.name, raw)
}

// stepThrough asks every key in turn, each defaulting to its current
// value: Enter keeps it, "-" removes it from the entry. A wrong value is
// asked again; the end of the input keeps the rest as it is.
func stepThrough(p *config.Project, filesDir string, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "Enter keeps the value in brackets, - removes the key.")
	scanner := bufio.NewScanner(in)
	for _, k := range projectKeys {
		for {
			fmt.Fprintf(out, "%s, %s [%s]: ", k.name, k.usage, shown(p, k.name))
			if !scanner.Scan() {
				fmt.Fprintln(out)
				return scanner.Err()
			}
			answer := strings.TrimSpace(scanner.Text())
			if answer == "" {
				break
			}
			if answer == "-" {
				field(p, k.name).SetZero()
				break
			}
			err := setValue(p, k, answer, filesDir)
			if err == nil {
				break
			}
			fmt.Fprintln(out, err)
		}
	}
	return nil
}

// projectCommand is acw project create / edit: a project's config entry
// written from flags, or question by question without any.
func projectCommand() *cobra.Command {
	project := &cobra.Command{
		Use:   "project",
		Short: "Create or change the per-project config entry of a repo",
	}
	project.AddCommand(projectWriteCommand())
	return project
}

// projectWriteCommand answers to create and edit alike: which one applies
// is acw's business, not the user's.
func projectWriteCommand() *cobra.Command {
	short := "Make or change a repo's config entry: only the flags given, or every key one question at a time without any"
	cmd := &cobra.Command{
		Use:     "edit [dir]",
		Aliases: []string{"create"},
		Short:   short,
		Long: short + `.

dir is the repo (default: the current directory). Files given to brief,
brief-extra and notes are copied into a folder of the repo's own next to
the config, and the entry points at the copy. presets and roles are
edited in the JSON.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := repoOrCwd(strings.Join(args, ""))
			if err != nil {
				return err
			}
			filesDir := config.FilesDir(repo, names.Slug(repo))
			err = config.Edit(repo, func(p *config.Project) error {
				given := false
				for _, k := range projectKeys {
					if !cmd.Flags().Changed(k.name) {
						continue
					}
					given = true
					if err := setValue(p, k, cmd.Flags().Lookup(k.name).Value.String(), filesDir); err != nil {
						return err
					}
				}
				if given {
					return nil
				}
				return stepThrough(p, filesDir, cmd.InOrStdin(), cmd.OutOrStdout())
			})
			if err != nil {
				return err
			}
			p, err := config.Load(repo)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config: %s → %s\n", config.Path(), p.Summary())
			return nil
		},
	}
	for _, k := range projectKeys {
		var probe config.Project
		switch field(&probe, k.name).Type().Elem().Kind() {
		case reflect.Int:
			cmd.Flags().Int(k.name, 0, k.usage)
		case reflect.Bool:
			cmd.Flags().Bool(k.name, false, k.usage)
		default:
			cmd.Flags().String(k.name, "", k.usage)
		}
	}
	return cmd
}
