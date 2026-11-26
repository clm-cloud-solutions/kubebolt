package kobiplan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Export rendering (plans-05): the text a person copies out of KubeBolt. Pure:
// the API gathers the release values and records the export; this decides
// what the text says. Each step is applied where its owner keeps it:
//   - nobody (or nobody identified): kubectl against the cluster, with the
//     rollback written under it; an unidentified owner carries the warning to
//     check nothing reverts it (P16);
//   - a Helm release: a values file with only the changed values, at the path
//     found in the release's own values, previewed with helm diff and applied
//     with helm upgrade --reuse-values; rolled back with helm rollback;
//   - Argo CD or Flux: the change to make in the repository, nothing to run.

// ReleaseValues are a Helm release's effective values (helm.EffectiveValues),
// keyed in ExportInput by "namespace/name".
type ReleaseValues struct {
	Chart        string
	ChartVersion string
	Revision     int
	Values       map[string]any
}

// ExportInput is what an export renders from.
type ExportInput struct {
	Plan        Plan
	Format      ExportFormat
	ClusterName string
	By          string
	At          time.Time
	// Releases holds the values of every Helm release the plan's steps
	// belong to that could be read. A missing one renders the change in
	// words instead of a values file.
	Releases map[string]*ReleaseValues
}

// Rendered is an export's text and what the person should know about it.
type Rendered struct {
	Format   ExportFormat `json:"format"`
	Filename string       `json:"filename"`
	Text     string       `json:"text"`
	// Notes are sentences the screen shows above the text: what to check
	// before running it.
	Notes []string `json:"notes"`
}

// Exportable says whether a plan can be exported now.
func (p Plan) Exportable() error {
	switch p.Status {
	case StatusExpired:
		return fmt.Errorf("the plan expired")
	case StatusApproved, StatusExecuting, StatusFailed:
		return fmt.Errorf("the plan is %s", p.Status)
	}
	return nil
}

// RecordExport writes an export into the plan: the record, the first
// export's time, and draft → exported. The baseline is the caller's (it is
// measured, not rendered). Once kept it never changes (P20); one the first
// export could not measure is taken by the next, for steps not yet applied.
func (p *Plan) RecordExport(format ExportFormat, by string, at time.Time, baseline *Baseline, stepBaselines map[string]*Baseline) {
	p.Exports = append(p.Exports, Export{Format: format, At: at, By: by})
	if p.ExportedAt == nil {
		t := at
		p.ExportedAt = &t
	}
	if p.Baseline == nil {
		p.Baseline = baseline
	}
	for i := range p.Steps {
		if s := &p.Steps[i]; s.Baseline == nil && s.Status == StepPending {
			s.Baseline = stepBaselines[s.ID]
		}
	}
	if p.Status == StatusDraft {
		p.Status = StatusExported
	}
}

// RenderExport renders a plan in commands or diff.
func RenderExport(in ExportInput) (Rendered, error) {
	if in.Format != ExportCommands && in.Format != ExportDiff {
		return Rendered{}, fmt.Errorf("unknown export format %q", in.Format)
	}
	r := renderer{in: in, groups: map[string]*releaseGroup{}}
	r.plan()
	ext := "sh"
	if in.Format == ExportDiff {
		ext = "yaml"
	}
	return Rendered{
		Format:   in.Format,
		Filename: fmt.Sprintf("kobi-plan-%s-%s.%s", in.Plan.ID, in.Format, ext),
		Text:     strings.TrimRight(r.b.String(), "\n") + "\n",
		Notes:    r.notes,
	}, nil
}

type renderer struct {
	in     ExportInput
	b      strings.Builder
	notes  []string
	groups map[string]*releaseGroup
	order  []string
	// counts for the notes
	unknown, helmUnplaced, gitopsSteps, poolSteps int
}

// releaseGroup is every step of one Helm release: one values file.
type releaseGroup struct {
	name, namespace string
	steps           []Step
}

type containerChange struct {
	change ContainerResources
	before ContainerResources
}

func (r *renderer) line(format string, a ...any) { fmt.Fprintf(&r.b, format+"\n", a...) }

func (r *renderer) plan() {
	p := r.in.Plan
	r.line("# Kobi Plan %q · version %d", p.Title, p.Version)
	where := r.in.ClusterName
	if where == "" {
		where = p.ClusterID
	}
	r.line("# Cluster %s · exported %s%s", where, r.in.At.UTC().Format("2006-01-02 15:04 UTC"), byWords(r.in.By))
	if p.Kind == KindConsolidation {
		r.line("# After each node goes, watch the pool for 7 days: it stays at its new size, no pod is left Pending, nothing is OOMKilled and restarts do not go up.")
	} else {
		r.line("# After each change, watch it for 24h: the rollout completes, nothing is OOMKilled and restarts do not go up.")
	}
	r.line("# Nothing here has run: review it before you apply it.")
	r.line("# These are KubeBolt's suggestions, not instructions: whether and how to apply each one is your decision.")
	switch {
	case p.Impact.Estimated.Money != nil && p.Kind == KindConsolidation:
		r.line("# Nodes are priced at OpenCost's list prices; with Savings Plans, reservations or committed-use discounts the bill falls by less.")
	case p.Impact.Estimated.Money != nil:
		r.line("# What a step frees is priced at list prices; it lowers the bill only once nodes or disks are removed.")
	}

	steps := make([]Step, 0, len(p.Steps))
	for _, s := range p.Steps {
		if s.Status != StepExpired {
			steps = append(steps, s)
		}
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Order < steps[j].Order })

	// Helm steps gather under their release, at the first step's place.
	for _, s := range steps {
		if o := s.Target.Owner; o != nil && o.Kind == OwnerHelm && o.Ref != "" {
			key := releaseKey(s)
			g, ok := r.groups[key]
			if !ok {
				g = &releaseGroup{name: o.Ref, namespace: releaseNamespace(s)}
				r.groups[key] = g
			}
			g.steps = append(g.steps, s)
		}
	}
	done := map[string]bool{}
	for _, s := range steps {
		helm := s.Target.Owner != nil && s.Target.Owner.Kind == OwnerHelm && s.Target.Owner.Ref != ""
		if helm && !s.ExportOnly && s.Action == ActionSetResources && done[releaseKey(s)] {
			continue // already written in its release's block
		}
		r.line("")
		switch {
		case s.Action == ActionRemoveNodes:
			r.pool(s)
		case s.ExportOnly || s.Action != ActionSetResources:
			r.line("# %s — %s", stepTitle(s), exportOnlyWords(s))
		case helm:
			key := releaseKey(s)
			done[key] = true
			r.release(r.groups[key])
		case s.Target.Owner != nil && s.Target.Owner.Kind == OwnerGitOps:
			r.gitops(s)
		default:
			r.cluster(s)
		}
	}

	if r.unknown > 0 {
		r.notes = append(r.notes, fmt.Sprintf("%s no identified owner: before applying, check that nothing reverts %s (Helm, Argo CD, Flux or another controller).",
			countWords(r.unknown, "step has", "steps have"), pick(r.unknown, "it", "them")))
	}
	if r.helmUnplaced > 0 {
		r.notes = append(r.notes, fmt.Sprintf("%s could not be placed in the release's values: the text says the change, and you place it.",
			countWords(r.helmUnplaced, "Helm change", "Helm changes")))
	}
	if len(r.groups) > 0 {
		r.notes = append(r.notes, "Helm changes go in the release's values: set CHART_… to the chart each release was installed from. The preview needs the helm-diff plugin.")
	}
	if r.poolSteps > 0 {
		r.notes = append(r.notes, "A pool's size lives in your infrastructure: whatever declares it (Terraform, eksctl, a pipeline) must say the new size too, or its next run brings the node back.")
	}
	if r.gitopsSteps > 0 {
		r.notes = append(r.notes, fmt.Sprintf("%s managed by Argo CD or Flux: make the change in the repository, the cluster would put it back.",
			countWords(r.gitopsSteps, "step is", "steps are")))
	}
}

// cluster renders a step applied against the cluster.
func (r *renderer) cluster(s Step) {
	changes, err := stepChanges(s)
	unknownOwner := s.Target.Owner == nil || s.Target.Owner.Kind == OwnerUnknown
	if unknownOwner {
		r.unknown++
	}
	r.line("# %s", stepTitle(s))
	if unknownOwner {
		r.line("# Nobody was identified as its owner: check that nothing reverts it (Helm, Argo CD, Flux or another controller).")
	}
	if err != nil {
		r.line("# %v", err)
		return
	}
	for _, c := range changes {
		r.line("#   %s", changeWords(c))
	}
	switch r.in.Format {
	case ExportCommands:
		for _, c := range changes {
			r.line("%s", kubectlCommand(s.Target, c.change))
		}
		if rb := rollbackChanges(s); len(rb) > 0 {
			r.line("# Roll back:")
			for _, c := range rb {
				r.line("# %s", kubectlCommand(s.Target, c))
			}
		}
	case ExportDiff:
		r.line("# Strategic merge patch on %s/%s:", singular(s.Target.Type), s.Target.Name)
		r.line("---")
		r.yaml(strategicPatch(changes))
	}
}

// release renders every step of one Helm release as one values file.
func (r *renderer) release(g *releaseGroup) {
	rv := r.in.Releases[g.namespace+"/"+g.name]
	head := fmt.Sprintf("# Helm release %s (namespace %s)", g.name, g.namespace)
	if rv != nil && rv.Chart != "" {
		head += fmt.Sprintf(" · chart %s %s", rv.Chart, rv.ChartVersion)
	}
	r.line("%s", head)
	r.line("# A change made in the cluster is undone by the next helm upgrade: it goes in the release's values.")
	patch := map[string]any{}
	var unplaced []string
	for _, s := range g.steps {
		r.line("# %s", stepTitle(s))
		changes, err := stepChanges(s)
		if err != nil {
			r.line("#   %v", err)
			continue
		}
		for _, c := range changes {
			var path []string
			ok := false
			if rv != nil {
				path, ok = FindResourcesPath(rv.Values, c.before, s.Target.Name, c.change.Container)
			}
			if ok {
				r.line("#   %s  (values: %s)", changeWords(c), strings.Join(path, "."))
				mergePatch(patch, ValuesPatch(path, c.change))
			} else {
				r.line("#   %s", changeWords(c))
				unplaced = append(unplaced, fmt.Sprintf("%s, %s", s.Target.Name, changeWords(c)))
				r.helmUnplaced++
			}
		}
	}
	file := fmt.Sprintf("%s-values-patch.yaml", g.name)
	if len(unplaced) > 0 {
		why := "the release's values could not be read"
		if rv != nil {
			why = "no block in the release's values sets exactly these resources"
		}
		r.line("# Not placed (%s): set these in the release's values yourself.", why)
		for _, u := range unplaced {
			r.line("#   %s", u)
		}
	}
	if len(patch) == 0 {
		return
	}
	version := ""
	if rv != nil && rv.ChartVersion != "" {
		version = " --version " + rv.ChartVersion
	}
	chartVar := "CHART_" + envName(g.name)
	switch r.in.Format {
	case ExportCommands:
		r.line("cat > %s <<'EOF'", file)
		r.yaml(patch)
		r.line("EOF")
		r.line("# Set %s to the chart release %s was installed from (repo/chart or an oci:// reference).", chartVar, g.name)
		r.line(": \"${%s:?set %s to the chart of release %s}\"", chartVar, chartVar, g.name)
		r.line("# Preview (helm-diff plugin):")
		r.line("helm diff upgrade %s \"$%s\"%s -n %s --reuse-values -f %s", g.name, chartVar, version, g.namespace, file)
		r.line("helm upgrade %s \"$%s\"%s -n %s --reuse-values -f %s", g.name, chartVar, version, g.namespace, file)
		r.line("# Roll back: helm rollback %s -n %s", g.name, g.namespace)
	case ExportDiff:
		r.line("# Values patch for release %s (%s):", g.name, file)
		r.line("---")
		r.yaml(patch)
	}
}

// gitops renders a step whose owner is Argo CD or Flux: words only.
func (r *renderer) gitops(s Step) {
	r.gitopsSteps++
	who := ownerName(s.Target.Owner)
	r.line("# %s — %s", stepTitle(s), who)
	r.line("# Make this change in the repository: %s puts back anything changed in the cluster.", who)
	changes, err := stepChanges(s)
	if err != nil {
		r.line("#   %v", err)
		return
	}
	for _, c := range changes {
		r.line("#   %s", changeWords(c))
	}
}

func (r *renderer) yaml(v any) {
	out, err := yaml.Marshal(v)
	if err != nil {
		r.line("# could not render: %v", err)
		return
	}
	r.b.Write(out)
}

// stepChanges pairs each container's change with its values before.
func stepChanges(s Step) ([]containerChange, error) {
	params, err := DecodeSetResources(s.Params)
	if err != nil {
		return nil, err
	}
	var before SetResourcesParams
	if len(s.Before) > 0 {
		if before, err = DecodeSetResources(s.Before); err != nil {
			return nil, err
		}
	}
	out := make([]containerChange, 0, len(params.Containers))
	for _, c := range params.Containers {
		cc := containerChange{change: c}
		for _, b := range before.Containers {
			if b.Container == c.Container && b.InitContainer == c.InitContainer {
				cc.before = b
				break
			}
		}
		out = append(out, cc)
	}
	return out, nil
}

func rollbackChanges(s Step) []ContainerResources {
	if s.Rollback == nil || len(s.Rollback.Params) == 0 {
		return nil
	}
	p, err := DecodeSetResources(s.Rollback.Params)
	if err != nil {
		return nil
	}
	return p.Containers
}

// changeWords: "container api: cpu request 500m → 120m, memory request 1Gi → 300Mi".
func changeWords(c containerChange) string {
	var parts []string
	add := func(kind string, to, from *Resources) {
		if to == nil {
			return
		}
		dim := func(name string, t, f *string) {
			if t == nil {
				return
			}
			was := "unset"
			if f != nil {
				was = *f
			}
			parts = append(parts, fmt.Sprintf("%s %s %s → %s", name, kind, was, *t))
		}
		var fc, fm *string
		if from != nil {
			fc, fm = from.CPU, from.Memory
		}
		dim("cpu", to.CPU, fc)
		dim("memory", to.Memory, fm)
	}
	add("request", c.change.Requests, c.before.Requests)
	add("limit", c.change.Limits, c.before.Limits)
	kind := "container"
	if c.change.InitContainer {
		kind = "init container"
	}
	return fmt.Sprintf("%s %s: %s", kind, c.change.Container, strings.Join(parts, ", "))
}

// kubectlCommand sets one container's resources. An init container goes
// through a patch: kubectl set resources does not reach init containers on
// every version.
func kubectlCommand(t Target, c ContainerResources) string {
	ref := singular(t.Type) + "/" + t.Name
	if c.InitContainer {
		raw, _ := json.Marshal(strategicPatch([]containerChange{{change: c}}))
		return fmt.Sprintf("kubectl -n %s patch %s --type=strategic -p %s", t.Namespace, ref, shellQuote(string(raw)))
	}
	cmd := fmt.Sprintf("kubectl -n %s set resources %s -c %s", t.Namespace, ref, c.Container)
	if q := quantityFlag(c.Requests); q != "" {
		cmd += " --requests=" + q
	}
	if q := quantityFlag(c.Limits); q != "" {
		cmd += " --limits=" + q
	}
	return cmd
}

// shellQuote wraps s in single quotes for a POSIX shell, a single quote
// inside it closed, escaped and reopened. The patch carries only a container
// name and quantities, which cannot hold a quote, but the text is meant to be
// pasted into a shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quantityFlag(r *Resources) string {
	if r == nil {
		return ""
	}
	var parts []string
	if r.CPU != nil {
		parts = append(parts, "cpu="+*r.CPU)
	}
	if r.Memory != nil {
		parts = append(parts, "memory="+*r.Memory)
	}
	return strings.Join(parts, ",")
}

// strategicPatch is the body set_resources sends for these changes.
func strategicPatch(changes []containerChange) map[string]any {
	var normal, init []any
	for _, c := range changes {
		res := map[string]any{}
		if m := quantityMap(c.change.Requests); len(m) > 0 {
			res["requests"] = m
		}
		if m := quantityMap(c.change.Limits); len(m) > 0 {
			res["limits"] = m
		}
		entry := map[string]any{"name": c.change.Container, "resources": res}
		if c.change.InitContainer {
			init = append(init, entry)
		} else {
			normal = append(normal, entry)
		}
	}
	spec := map[string]any{}
	if len(normal) > 0 {
		spec["containers"] = normal
	}
	if len(init) > 0 {
		spec["initContainers"] = init
	}
	return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}
}

func stepTitle(s Step) string {
	ns := ""
	if s.Target.Namespace != "" {
		ns = s.Target.Namespace + " / "
	}
	return fmt.Sprintf("%d · %s%s (%s)", s.Order, ns, s.Target.Name, singular(s.Target.Type))
}

func exportOnlyWords(s Step) string {
	if s.ExportOnlyReason != "" {
		return "no command for this change yet: " + s.ExportOnlyReason
	}
	return "no command for this change yet"
}

func singular(t string) string {
	switch t {
	case "deployments":
		return "deployment"
	case "statefulsets":
		return "statefulset"
	case "daemonsets":
		return "daemonset"
	}
	return strings.TrimSuffix(t, "s")
}

func releaseNamespace(s Step) string {
	if s.Target.Owner != nil && s.Target.Owner.Namespace != "" {
		return s.Target.Owner.Namespace
	}
	return s.Target.Namespace
}

func releaseKey(s Step) string { return releaseNamespace(s) + "/" + s.Target.Owner.Ref }

func ownerName(o *Owner) string {
	if o == nil {
		return "its owner"
	}
	ref := o.Ref
	switch {
	case strings.HasPrefix(ref, "argocd:"):
		return "Argo CD (application " + strings.TrimPrefix(ref, "argocd:") + ")"
	case strings.HasPrefix(ref, "flux:"):
		return "Flux (" + strings.TrimPrefix(ref, "flux:") + ")"
	case ref != "":
		return ref
	}
	return "its GitOps controller"
}

// envName turns a release name into a shell variable suffix.
func envName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func byWords(by string) string {
	if by == "" {
		return ""
	}
	return " by " + by
}

func countWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
