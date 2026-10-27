package skills

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLibraryLoads(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("no skills embedded")
	}
	s, ok := Get("workload-revisions")
	if !ok {
		t.Fatal("workload-revisions is missing")
	}
	if !strings.Contains(Index(), "- workload-revisions: "+s.Description) {
		t.Errorf("the index does not list it:\n%s", Index())
	}
	if strings.HasPrefix(s.Body, "---") || strings.Contains(s.Body, "description:") {
		t.Errorf("the frontmatter leaked into the body:\n%s", s.Body)
	}
}

// A skill the code preloads is loadable by name but kept out of the index,
// which every agent pays for on every turn.
func TestUnlistedSkillsStayOutOfTheIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"library/listed-one/SKILL.md":    {Data: []byte("---\nname: listed-one\ndescription: d1\n---\nb\n")},
		"library/preloaded-one/SKILL.md": {Data: []byte("---\nname: preloaded-one\ndescription: d2\nlisted: false\n---\nb\n")},
	}
	got, err := load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Listed || got[1].Listed {
		t.Errorf("listed flags: %+v", got)
	}
	for _, s := range All() {
		if !s.Listed && strings.Contains(Index(), s.Name) {
			t.Errorf("%s is unlisted but in the index", s.Name)
		}
		if _, ok := Get(s.Name); !ok {
			t.Errorf("%s cannot be loaded by name", s.Name)
		}
	}
}

// A preloaded skill rides in the task of every run of its rule; one that
// grows past a page is a manual, not a procedure. Kobi's own skills are never
// preloaded — the chat loads one when a question needs it, once per
// conversation, instead of paying it on every turn in the prompt it came
// from — so one may hold a chapter of that prompt.
func TestSkillsStayShort(t *testing.T) {
	for _, s := range All() {
		limit := 2000
		if s.Audience == AudienceKobi {
			limit = 10000
		}
		if len(s.Body) > limit {
			t.Errorf("%s is %d bytes: keep it under %d", s.Name, len(s.Body), limit)
		}
	}
}

// A skill for one consumer is listed only in that consumer's index.
func TestAudienceNarrowsTheIndex(t *testing.T) {
	seen := false
	for _, s := range All() {
		if s.Audience == "" || !s.Listed {
			continue
		}
		seen = true
		if strings.Contains(Index(), "- "+s.Name+":") {
			t.Errorf("%s (audience %s) is in the shared index", s.Name, s.Audience)
		}
		if !strings.Contains(IndexFor(s.Audience), "- "+s.Name+":") {
			t.Errorf("%s is not in its audience's index", s.Name)
		}
	}
	if !seen {
		t.Skip("no audience-specific skill")
	}
	for _, line := range strings.Split(Index(), "\n") {
		if !strings.Contains(IndexFor(AudienceKobi), line) {
			t.Errorf("Kobi's index lacks the shared %q", line)
		}
	}
}

func TestMalformedSkillsAreRefused(t *testing.T) {
	ok := "---\nname: a-skill\ndescription: when to use it\n---\nbody\n"
	for name, tc := range map[string]struct {
		dir, raw string
	}{
		"no frontmatter":      {"a-skill", "body only\n"},
		"unterminated":        {"a-skill", "---\nname: a-skill\n"},
		"no description":      {"a-skill", "---\nname: a-skill\n---\nbody\n"},
		"empty body":          {"a-skill", "---\nname: a-skill\ndescription: d\n---\n\n"},
		"not kebab-case":      {"A_Skill", "---\nname: A_Skill\ndescription: d\n---\nbody\n"},
		"name is not its dir": {"other", ok},
	} {
		fsys := fstest.MapFS{"library/" + tc.dir + "/SKILL.md": {Data: []byte(tc.raw)}}
		if _, err := load(fsys); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if got, err := load(fstest.MapFS{"library/a-skill/SKILL.md": {Data: []byte(ok)}}); err != nil || got[0].Body != "body" {
		t.Errorf("a well-formed skill: %v %+v", err, got)
	}
}
