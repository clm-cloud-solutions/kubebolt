// Package skills holds KubeBolt's own procedures as SKILL.md files — the open
// skills format: a name, a one-line description of when to use it, and a
// body — and serves them to the agents through the load_skill tool.
//
// One source for every consumer: Kobi reads them in process, Autopilot over
// the KubeBolt MCP server, whatever engine runs it. The agents see an index
// (name and description, in load_skill's own description) and load a body
// only when the situation calls for it, so a procedure costs nothing on the
// turns that do not need it.
//
// What goes here is OUR procedure — how KubeBolt's tools fit together, what
// its remedies do — never general domain knowledge the model already has,
// and never a safeguard: those stay in code, and no skill overrides one.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:embed library/*/SKILL.md
var library embed.FS

// Skill is one procedure.
type Skill struct {
	Name        string
	Description string
	Body        string
	// Listed puts it in the index the agents read. A skill the code always
	// preloads (an incident type's procedure) is not listed: the index is on
	// every turn of every agent, and an entry there costs its description
	// each time for something no model has to discover. It can still be
	// loaded by name. Frontmatter "listed: false"; listed by default.
	Listed bool
	// Audience narrows the index a skill is listed in to one consumer
	// ("kobi"): a procedure built on tools only the chat has (the cluster
	// switch, the connectors' handoff) would cost Autopilot's agents its
	// description on every turn for nothing. Empty: every consumer.
	Audience string
}

var (
	all    []Skill
	byName map[string]Skill
)

func init() {
	var err error
	if all, err = load(library); err != nil {
		// A malformed skill is a build mistake: fail the boot, not a run.
		panic(err)
	}
	byName = make(map[string]Skill, len(all))
	for _, s := range all {
		byName[s.Name] = s
	}
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func load(fsys fs.FS) ([]Skill, error) {
	files, err := fs.Glob(fsys, "library/*/SKILL.md")
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(files))
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		s, err := parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", f, err)
		}
		if dir := path.Base(path.Dir(f)); s.Name != dir {
			return nil, fmt.Errorf("skill %s: name %q must match its directory %q", f, s.Name, dir)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parse reads the frontmatter (name, description) and the body.
func parse(raw string) (Skill, error) {
	rest, ok := strings.CutPrefix(raw, "---\n")
	if !ok {
		return Skill{}, fmt.Errorf("missing frontmatter")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Skill{}, fmt.Errorf("unterminated frontmatter")
	}
	s := Skill{Listed: true}
	for _, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = strings.TrimSpace(value)
		case "description":
			s.Description = strings.TrimSpace(value)
		case "listed":
			s.Listed = strings.TrimSpace(value) != "false"
		case "audience":
			s.Audience = strings.TrimSpace(value)
		}
	}
	s.Body = strings.TrimSpace(body)
	switch {
	case !validName.MatchString(s.Name):
		return Skill{}, fmt.Errorf("name %q is not kebab-case", s.Name)
	case s.Description == "":
		return Skill{}, fmt.Errorf("no description: the index is how an agent decides to load it")
	case s.Body == "":
		return Skill{}, fmt.Errorf("empty body")
	}
	return s, nil
}

// All returns every skill, sorted by name.
func All() []Skill { return append([]Skill(nil), all...) }

// Get returns a skill by name.
func Get(name string) (Skill, bool) {
	s, ok := byName[name]
	return s, ok
}

// Names returns the skill names, sorted.
func Names() []string {
	names := make([]string, 0, len(all))
	for _, s := range all {
		names = append(names, s.Name)
	}
	return names
}

// AudienceKobi is the chat's own skills (frontmatter "audience: kobi").
const AudienceKobi = "kobi"

// Index is one line per listed skill every consumer shares — what an agent
// reads to decide what to load.
func Index() string { return IndexFor("") }

// IndexFor is the index one consumer reads: the shared skills plus its own.
func IndexFor(audience string) string {
	lines := make([]string, 0, len(all))
	for _, s := range all {
		if s.Listed && (s.Audience == "" || s.Audience == audience) {
			lines = append(lines, "- "+s.Name+": "+s.Description)
		}
	}
	return strings.Join(lines, "\n")
}
