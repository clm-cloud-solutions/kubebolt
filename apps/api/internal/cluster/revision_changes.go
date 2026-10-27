package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// What each revision of a workload changed, for get_workload_history.
//
// The history rows alone (number, replicas, first image, createdAt) cannot say
// WHAT a rollout changed, and a replaced revision's replicas / readyReplicas
// are its current counts (0), which a reader took for "that revision was
// NotReady" and advised against the one rollback that fixed the incident: the
// newer revision had added FAULT_MODE=error and nothing else. So the tool's
// rows carry the pod-template diff against the revision before, the moment a
// newer revision replaced them, and the change-cause annotation. The REST
// history endpoints keep their plain rows — the History tab has its own diff
// view (history.go's ManifestYAML).

const (
	// maxRevisionChangeLines bounds one revision's change list, the "… N more
	// changes" line included: a revision that rewrote the whole template is
	// read in the YAML, not here.
	maxRevisionChangeLines = 12
	// maxChangeValueLen clips every value a change line quotes (env values,
	// images, ref names) so one long value cannot dominate the tool result.
	maxChangeValueLen = 80
	// maxChangeLineBytes bounds a finished change line in bytes, after
	// quoting (a non-printable rune quotes to ten bytes): with
	// maxRevisionChangeLines it is a hard bound of about 4 KB per revision.
	maxChangeLineBytes = 320
	// restartedAtAnnotation is what `kubectl rollout restart` writes on the pod
	// template — the whole change of a restart revision.
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
	// revisionHistoryAnnotation lists the revision numbers a ReplicaSet held
	// before a rollback gave it its current one ("2" or "2,5").
	revisionHistoryAnnotation = "deployment.kubernetes.io/revision-history"
	hiddenValue               = "(value hidden)"
)

// revisionRow is one revision as the history reads it: the row the REST
// endpoint returns, plus what the tool derives from it.
type revisionRow struct {
	item     map[string]interface{}
	revision int64
	// earlier are the numbers this object held before its current one, from a
	// ReplicaSet's revision-history annotation, ascending. A ControllerRevision
	// keeps none: a rollback just renumbers it.
	earlier []int64
	created time.Time
	cause   string
	// template is nil when the revision's pod template could not be read.
	template *corev1.PodTemplateSpec
}

// firstRevision is the number under which this object first rolled out — the
// one its createdAt belongs to.
func (r revisionRow) firstRevision() int64 {
	if len(r.earlier) > 0 && r.earlier[0] < r.revision {
		return r.earlier[0]
	}
	return r.revision
}

// GetWorkloadHistoryWithChanges is get_workload_history's read: the rows
// GetDeploymentHistory / GetWorkloadHistory return, newest first, each with
// `changes` (what its pod template changed against the revision before it),
// `supersededAt` (when the rollout to the next revision started) and
// `changeCause` (when the annotation is set). Deployments read the ReplicaSet
// lister (no API call); StatefulSets and DaemonSets the ControllerRevision
// LIST the plain history already makes.
func (c *Connector) GetWorkloadHistoryWithChanges(resourceType, namespace, name string) []map[string]interface{} {
	if resourceType == "deployments" {
		if c.replicaSetLister == nil {
			return nil
		}
		replicaSets, _ := c.replicaSetLister.List(everythingSelector())
		return DeploymentHistoryWithChanges(replicaSets, namespace, name)
	}
	return annotateRevisionRows(c.controllerRevisionRows(resourceType, namespace, name))
}

// DeploymentHistoryWithChanges builds the tool's rows for a Deployment from
// ReplicaSets — any slice: other namespaces and other owners are skipped. The
// connector feeds it its lister; a test can feed it objects.
func DeploymentHistoryWithChanges(replicaSets []*appsv1.ReplicaSet, namespace, name string) []map[string]interface{} {
	return annotateRevisionRows(deploymentRevisionRows(replicaSets, namespace, name))
}

// deploymentRevisionRows is the Deployment history both readers share, sorted
// by revision number, newest first. Each item is exactly what
// GetDeploymentHistory has always returned.
func deploymentRevisionRows(replicaSets []*appsv1.ReplicaSet, namespace, deploymentName string) []revisionRow {
	var rows []revisionRow
	for _, rs := range replicaSets {
		if rs.Namespace != namespace {
			continue
		}
		ownedByDeployment := false
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == deploymentName {
				ownedByDeployment = true
				break
			}
		}
		if !ownedByDeployment {
			continue
		}

		revision := ""
		if rs.Annotations != nil {
			revision = rs.Annotations[deploymentRevisionAnnotation]
		}

		image := ""
		if len(rs.Spec.Template.Spec.Containers) > 0 {
			image = rs.Spec.Template.Spec.Containers[0].Image
		}

		rev, _ := strconv.Atoi(revision)
		rows = append(rows, revisionRow{
			item: map[string]interface{}{
				"revision":      revision,
				"name":          rs.Name,
				"namespace":     rs.Namespace,
				"replicas":      rs.Status.Replicas,
				"readyReplicas": rs.Status.ReadyReplicas,
				"createdAt":     rs.CreationTimestamp.Format(time.RFC3339),
				"age":           formatAge(rs.CreationTimestamp.Time),
				"image":         image,
				"active":        rs.Status.Replicas > 0,
			},
			revision: int64(rev),
			earlier:  revisionHistory(rs.Annotations[revisionHistoryAnnotation]),
			created:  rs.CreationTimestamp.Time,
			cause:    rs.Annotations[changeCauseAnnotation],
			template: &rs.Spec.Template,
		})
	}

	// Sort by revision number descending (highest/newest first)
	sort.Slice(rows, func(i, j int) bool { return rows[i].revision > rows[j].revision })
	return rows
}

// revisionHistory parses the revision-history annotation ("2,5"), ascending;
// what does not parse is skipped.
func revisionHistory(annotation string) []int64 {
	var out []int64
	for _, f := range strings.Split(annotation, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// controllerRevisionRows is the StatefulSet / DaemonSet history both readers
// share: one live ControllerRevision LIST, newest first. Each item is exactly
// what GetWorkloadHistory has always returned.
func (c *Connector) controllerRevisionRows(resourceType, namespace, name string) []revisionRow {
	var kind string
	switch resourceType {
	case "statefulsets":
		kind = "StatefulSet"
	case "daemonsets":
		kind = "DaemonSet"
	default:
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	revisions, err := c.clientset.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}

	var rows []revisionRow
	for i := range revisions.Items {
		rev := &revisions.Items[i]
		if !isOwnedByKindName(rev.OwnerReferences, kind, name) {
			continue
		}
		rows = append(rows, revisionRow{
			item: map[string]interface{}{
				"revision":  rev.Revision,
				"name":      rev.Name,
				"createdAt": rev.CreationTimestamp.Format(time.RFC3339),
				"age":       formatAge(rev.CreationTimestamp.Time),
			},
			revision: rev.Revision,
			created:  rev.CreationTimestamp.Time,
			cause:    rev.Annotations[changeCauseAnnotation],
			template: controllerRevisionTemplate(rev.Data.Raw),
		})
	}

	// Sort by revision descending (newest first)
	sort.Slice(rows, func(i, j int) bool { return rows[i].revision > rows[j].revision })
	return rows
}

// controllerRevisionTemplate decodes the pod template a StatefulSet /
// DaemonSet ControllerRevision carries: a patch shaped
// {"spec":{"template":{"$patch":"replace",…}}} whose template is complete.
// nil when it cannot be read — an empty template would read as "every
// container added".
func controllerRevisionTemplate(raw []byte) *corev1.PodTemplateSpec {
	if len(raw) == 0 {
		return nil
	}
	var partial struct {
		Spec struct {
			Template *corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &partial); err != nil {
		return nil
	}
	return partial.Spec.Template
}

// annotateRevisionRows adds the tool's fields to rows sorted newest first and
// returns the items (nil for no rows, as the plain history does).
//
// A revision's neighbours are found by NUMBER, not by list position: a
// rollback re-uses an older object under a new number, so the number it held
// leaves a hole, and the rows beside the hole are not each other's
// neighbours — diffing across it dated a fault to the wrong revision. A
// ReplicaSet also answers for the numbers in its revision-history; a
// ControllerRevision keeps none, so its hole stays a hole and nothing is
// diffed or dated across it.
func annotateRevisionRows(rows []revisionRow) []map[string]interface{} {
	idx := revisionIndex{rows: rows, holder: make(map[int64]int, len(rows))}
	hold := func(n int64, i int) {
		if n <= 0 {
			return
		}
		if _, taken := idx.holder[n]; !taken {
			idx.holder[n] = i
		}
		if idx.oldest == 0 || n < idx.oldest {
			idx.oldest = n
		}
	}
	for i, r := range rows { // current numbers first: they win over history
		hold(r.revision, i)
	}
	for i, r := range rows {
		for _, n := range r.earlier {
			hold(n, i)
		}
	}

	var items []map[string]interface{}
	for i, r := range rows {
		item := r.item
		if r.cause != "" {
			item["changeCause"] = changeCauseDesc(r.cause)
		}
		if r.revision > 0 {
			if at, ok := idx.supersededAt(i); ok {
				item["supersededAt"] = at
			}
			if r.revision > idx.oldest {
				item["changes"] = capChangeLines(idx.changeLines(i))
			}
		}
		items = append(items, item)
	}
	return items
}

// revisionIndex finds the object that held a revision number.
type revisionIndex struct {
	rows   []revisionRow
	holder map[int64]int
	oldest int64 // the smallest number anyone holds: it has no predecessor
}

// supersededAt is when the rollout from row i's revision n to n+1 started: the
// createdAt of the object holding n+1, only when n+1 was that object's first
// rollout. A rollback re-uses an older object whose createdAt is when it first
// rolled out, not when it came back; the created check covers the objects that
// keep no revision-history.
func (x revisionIndex) supersededAt(i int) (interface{}, bool) {
	r := x.rows[i]
	j, ok := x.holder[r.revision+1]
	if !ok || j == i {
		return nil, false
	}
	next := x.rows[j]
	if next.firstRevision() != r.revision+1 || next.created.Before(r.created) {
		return nil, false
	}
	return next.item["createdAt"], true
}

// changeLines is row i's change list: against the object that held the number
// before its current one and — for a rollback whose history says under which
// number the template first rolled out — what it changed then, which would
// otherwise show nowhere.
func (x revisionIndex) changeLines(i int) []string {
	r := x.rows[i]
	j, havePrev := x.holder[r.revision-1]
	first := r.firstRevision()
	var lines []string
	switch {
	case first < r.revision:
		lines = append(lines, fmt.Sprintf("rollback to revision %d's template: createdAt is when it first rolled out, not when it came back", first))
	case havePrev && r.created.Before(x.rows[j].created):
		lines = append(lines, "rollback to an earlier template: createdAt is when it first rolled out, not when it came back")
	}
	switch {
	case !havePrev:
		lines = append(lines, fmt.Sprintf("changes unavailable: revision %d is no longer in the history (re-used by a later rollback, or deleted)", r.revision-1))
	case j != i:
		lines = append(lines, podTemplateChangeLines(x.rows[j].template, r.template)...)
	}
	if first < r.revision && first > x.oldest {
		if k, ok := x.holder[first-1]; ok && k != i {
			for _, l := range podTemplateChangeLines(x.rows[k].template, r.template) {
				lines = append(lines, fmt.Sprintf("as revision %d: %s", first, l))
			}
		}
	}
	return lines
}

// changeCauseDesc is the change-cause annotation as the tool shows it. It is
// often the recorded command line, and `kubectl set env … DATABASE_URL=…` or
// `helm upgrade --set db.pass=…` records the value too: a `KEY=value` word
// whose key or value looks like a credential loses its value, so does the word
// after one named like a credential ("--token x", "Bearer x", "Authorization:
// Basic x"), and a cause whose remaining words still carry a credential shape
// is hidden whole.
func changeCauseDesc(cause string) string {
	words := strings.Fields(cause)
	var kept []string
	hideNext := false
	for i, w := range words {
		if hideNext {
			// A scheme word ("Bearer", "Basic") is followed by the credential.
			hideNext = looksLikeCredentialName(w) || strings.EqualFold(w, "basic")
			words[i] = hiddenValue
			continue
		}
		if k, v, ok := strings.Cut(w, "="); ok && v != "" && sensitiveEnv(k, v) {
			words[i] = k + "=" + hiddenValue
			continue
		}
		kept = append(kept, w)
		hideNext = looksLikeCredentialName(w) || looksLikeSensitiveKey(w)
	}
	if looksLikeCredentialValue(strings.Join(kept, " ")) {
		return hiddenValue
	}
	return revClip(strings.Join(words, " "))
}

// capChangeLines keeps at most maxRevisionChangeLines lines, the last one
// saying how many were left out, each at most maxChangeLineBytes.
func capChangeLines(lines []string) []string {
	n := len(lines)
	if n > maxRevisionChangeLines {
		n = maxRevisionChangeLines - 1
	}
	out := make([]string, 0, n+1)
	for _, l := range lines[:n] {
		out = append(out, clipLine(l))
	}
	if n < len(lines) {
		out = append(out, fmt.Sprintf("… %d more changes", len(lines)-n))
	}
	return out
}

// clipLine cuts a line at maxChangeLineBytes on a rune boundary.
func clipLine(s string) string {
	if len(s) <= maxChangeLineBytes {
		return s
	}
	cut := maxChangeLineBytes - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// podTemplateChangeLines says how cur differs from prev (the next older
// revision's template), one short line per change, uncapped. Values are
// clipped, and an env value is shown only when neither its name nor the value
// looks sensitive; valueFrom shows only the kind of source and its ref names —
// a Secret is never read.
func podTemplateChangeLines(prev, cur *corev1.PodTemplateSpec) []string {
	if prev == nil || cur == nil {
		return []string{"changes unavailable: a revision's pod template could not be read"}
	}
	var out []string
	out = append(out, containerListChanges("container", prev.Spec.Containers, cur.Spec.Containers)...)
	out = append(out, containerListChanges("initContainer", prev.Spec.InitContainers, cur.Spec.InitContainers)...)
	out = append(out, volumeChanges(prev.Spec.Volumes, cur.Spec.Volumes)...)
	if a, b := serviceAccountOrDefault(prev.Spec.ServiceAccountName), serviceAccountOrDefault(cur.Spec.ServiceAccountName); a != b {
		out = append(out, fmt.Sprintf("serviceAccountName %s → %s", revClip(a), revClip(b)))
	}
	if line := restartChange(prev.Annotations[restartedAtAnnotation], cur.Annotations[restartedAtAnnotation]); line != "" {
		out = append(out, line)
	}
	if keys := changedKeys(prev.Annotations, cur.Annotations, restartedAtAnnotation); keys != "" {
		out = append(out, "pod annotations changed: "+keys)
	}
	if keys := changedKeys(prev.Labels, cur.Labels, podTemplateHashLabel); keys != "" {
		out = append(out, "pod labels changed: "+keys)
	}
	if len(out) == 0 {
		out = append(out, "other pod template fields changed (not summarized here)")
	}
	return out
}

// restartChange reads the restartedAt annotation between two revisions. A
// rollback carries the older template's value back (or none): that is not a
// new restart. The value is printed only as the time it parses to — the
// annotation is free text.
func restartChange(prev, cur string) string {
	if cur == prev {
		return ""
	}
	if cur == "" {
		return "restartedAt annotation removed"
	}
	curT, curErr := time.Parse(time.RFC3339, cur)
	prevT, prevErr := time.Parse(time.RFC3339, prev)
	switch {
	case curErr != nil && prev == "":
		return "restart requested (rollout restart)"
	case curErr != nil || (prev != "" && prevErr != nil):
		return "restartedAt annotation changed"
	case prev == "" || !curT.Before(prevT):
		return "restart requested (rollout restart) at " + curT.Format(time.RFC3339)
	}
	return fmt.Sprintf("restartedAt annotation back to %s (was %s)", curT.Format(time.RFC3339), prevT.Format(time.RFC3339))
}

// containerListChanges diffs two container lists by name: changes in place,
// then added (in the new order), then removed.
func containerListChanges(kind string, prev, cur []corev1.Container) []string {
	prevByName := make(map[string]*corev1.Container, len(prev))
	for i := range prev {
		prevByName[prev[i].Name] = &prev[i]
	}
	inCur := make(map[string]bool, len(cur))
	var out []string
	for i := range cur {
		c := &cur[i]
		inCur[c.Name] = true
		label := kind + " " + c.Name
		p, ok := prevByName[c.Name]
		if !ok {
			out = append(out, fmt.Sprintf("%s added (image %s)", label, revClip(c.Image)))
			continue
		}
		out = append(out, containerChanges(label, p, c)...)
	}
	for i := range prev {
		if !inCur[prev[i].Name] {
			out = append(out, fmt.Sprintf("%s %s removed", kind, prev[i].Name))
		}
	}
	return out
}

func containerChanges(label string, p, c *corev1.Container) []string {
	var out []string
	if p.Image != c.Image {
		out = append(out, fmt.Sprintf("%s: image %s → %s", label, revClip(p.Image), revClip(c.Image)))
	}
	out = append(out, envChanges(label, p.Env, c.Env)...)
	out = append(out, envFromChanges(label, p.EnvFrom, c.EnvFrom)...)
	if line := commandChange("command", p.Command, c.Command); line != "" {
		out = append(out, label+": "+line)
	}
	if line := commandChange("args", p.Args, c.Args); line != "" {
		out = append(out, label+": "+line)
	}
	out = append(out, quantityChanges(label, "requests", p.Resources.Requests, c.Resources.Requests)...)
	out = append(out, quantityChanges(label, "limits", p.Resources.Limits, c.Resources.Limits)...)
	probes := []struct {
		name string
		p, c *corev1.Probe
	}{
		{"readinessProbe", p.ReadinessProbe, c.ReadinessProbe},
		{"livenessProbe", p.LivenessProbe, c.LivenessProbe},
		{"startupProbe", p.StartupProbe, c.StartupProbe},
	}
	for _, pr := range probes {
		if line := probeChange(pr.p, pr.c); line != "" {
			out = append(out, fmt.Sprintf("%s: %s %s", label, pr.name, line))
		}
	}
	return out
}

// envChanges diffs env by name (the last entry of a name wins, as in the
// kubelet): changed and added in the new order, then removed.
func envChanges(label string, prev, cur []corev1.EnvVar) []string {
	prevByName, prevOrder := envIndex(prev)
	curByName, curOrder := envIndex(cur)
	var out []string
	for _, name := range curOrder {
		c := curByName[name]
		p, ok := prevByName[name]
		cd, cLiteral := envDesc(c)
		shown := revName(name)
		switch {
		case !ok && cLiteral:
			out = append(out, fmt.Sprintf("%s: env %s added = %s", label, shown, cd))
		case !ok:
			out = append(out, fmt.Sprintf("%s: env %s added %s", label, shown, cd))
		case p.Value != c.Value || !equality.Semantic.DeepEqual(p.ValueFrom, c.ValueFrom):
			pd, _ := envDesc(p)
			// A literal that moved into a Secret (or out of one) was a
			// credential: that plaintext is never shown beside the ref.
			if fromSecret(p) || fromSecret(c) {
				if p.ValueFrom == nil {
					pd = hiddenValue
				}
				if c.ValueFrom == nil {
					cd = hiddenValue
				}
			}
			if pd == hiddenValue && cd == hiddenValue {
				out = append(out, fmt.Sprintf("%s: env %s changed %s", label, shown, hiddenValue))
			} else {
				out = append(out, fmt.Sprintf("%s: env %s changed: %s → %s", label, shown, pd, cd))
			}
		}
	}
	for _, name := range prevOrder {
		if _, ok := curByName[name]; !ok {
			out = append(out, fmt.Sprintf("%s: env %s removed", label, revName(name)))
		}
	}
	return out
}

func fromSecret(e corev1.EnvVar) bool {
	return e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil
}

func envIndex(env []corev1.EnvVar) (map[string]corev1.EnvVar, []string) {
	byName := make(map[string]corev1.EnvVar, len(env))
	var order []string
	for _, e := range env {
		if _, seen := byName[e.Name]; !seen {
			order = append(order, e.Name)
		}
		byName[e.Name] = e
	}
	return byName, order
}

// envDesc renders one env entry's value for a change line. literal is true
// for a shown plain value (rendered quoted, which also escapes the runes that
// print nothing).
func envDesc(e corev1.EnvVar) (desc string, literal bool) {
	if e.ValueFrom != nil {
		return "from " + valueFromDesc(e.ValueFrom), false
	}
	if sensitiveEnv(e.Name, e.Value) {
		return hiddenValue, false
	}
	return strconv.Quote(clipMiddle(e.Value)), true
}

// SensitiveEnv is sensitiveEnv (redact.go) for readers outside the package:
// Kobi hides the same values from what it reads (copilot.HideCredentialEnv).
func SensitiveEnv(name, value string) bool { return sensitiveEnv(name, value) }

// valueFromDesc names the source kind and its refs, never a value.
func valueFromDesc(v *corev1.EnvVarSource) string {
	switch {
	case v.SecretKeyRef != nil:
		return fmt.Sprintf("secret %s key %s", revClip(v.SecretKeyRef.Name), revClip(v.SecretKeyRef.Key))
	case v.ConfigMapKeyRef != nil:
		return fmt.Sprintf("configMap %s key %s", revClip(v.ConfigMapKeyRef.Name), revClip(v.ConfigMapKeyRef.Key))
	case v.FieldRef != nil:
		return "field " + revClip(v.FieldRef.FieldPath)
	case v.ResourceFieldRef != nil:
		return "resource " + revClip(v.ResourceFieldRef.Resource)
	case v.FileKeyRef != nil:
		return fmt.Sprintf("file %s/%s key %s", revClip(v.FileKeyRef.VolumeName), revClip(v.FileKeyRef.Path), revClip(v.FileKeyRef.Key))
	}
	return "another source"
}

// envFromChanges compares envFrom as a set of refs.
func envFromChanges(label string, prev, cur []corev1.EnvFromSource) []string {
	prevSet := make(map[string]bool, len(prev))
	for _, s := range prev {
		prevSet[envFromDesc(s)] = true
	}
	curSet := make(map[string]bool, len(cur))
	var out []string
	for _, s := range cur {
		d := envFromDesc(s)
		curSet[d] = true
		if !prevSet[d] {
			out = append(out, fmt.Sprintf("%s: envFrom %s added", label, d))
		}
	}
	for _, s := range prev {
		if d := envFromDesc(s); !curSet[d] {
			out = append(out, fmt.Sprintf("%s: envFrom %s removed", label, d))
		}
	}
	return out
}

func envFromDesc(s corev1.EnvFromSource) string {
	d := "another source"
	switch {
	case s.ConfigMapRef != nil:
		d = "configMap " + revClip(s.ConfigMapRef.Name)
	case s.SecretRef != nil:
		d = "secret " + revClip(s.SecretRef.Name)
	}
	if s.Prefix != "" {
		d += " (prefix " + revName(s.Prefix) + ")"
	}
	return d
}

// commandChange says that command / args changed and how many elements each
// has — never their content: an argument after "-p" is a port or a password
// and nothing in the list tells which. A command also names the program it
// runs when that reads as a plain path.
func commandChange(field string, prev, cur []string) string {
	if equalStrings(prev, cur) {
		return ""
	}
	if len(cur) == 0 {
		return field + " removed"
	}
	line := fmt.Sprintf("%s changed (elements: %d → %d", field, len(prev), len(cur))
	if field == "command" {
		if p := programName(cur); p != "" {
			line += ", runs " + p
		}
	}
	return line + ")"
}

var plainProgram = regexp.MustCompile(`^[A-Za-z0-9_./+-]{1,60}$`)

// programName is the first element of a command when it is a plain path — the
// program, never an argument.
func programName(cmd []string) string {
	if len(cmd) == 0 || !plainProgram.MatchString(cmd[0]) || looksLikeCredentialValue(cmd[0]) {
		return ""
	}
	return cmd[0]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// quantityChanges diffs requests or limits per resource name.
func quantityChanges(label, field string, prev, cur corev1.ResourceList) []string {
	names := make(map[corev1.ResourceName]bool, len(prev)+len(cur))
	for n := range prev {
		names[n] = true
	}
	for n := range cur {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, string(n))
	}
	sort.Strings(sorted)
	var out []string
	for _, n := range sorted {
		p, inPrev := prev[corev1.ResourceName(n)]
		c, inCur := cur[corev1.ResourceName(n)]
		switch {
		case inPrev && inCur:
			if p.Cmp(c) != 0 {
				out = append(out, fmt.Sprintf("%s: %s.%s %s → %s", label, field, n, p.String(), c.String()))
			}
		case inCur:
			out = append(out, fmt.Sprintf("%s: %s.%s set to %s", label, field, n, c.String()))
		default:
			out = append(out, fmt.Sprintf("%s: %s.%s removed (was %s)", label, field, n, p.String()))
		}
	}
	return out
}

// probeChange compacts one probe's difference into a line tail: "added (…)",
// "removed", or the fields that moved.
func probeChange(p, c *corev1.Probe) string {
	switch {
	case p == nil && c == nil:
		return ""
	case p == nil:
		return fmt.Sprintf("added (%s, timeout %ds, period %ds, failureThreshold %d)",
			probeHandlerDesc(c), c.TimeoutSeconds, c.PeriodSeconds, c.FailureThreshold)
	case c == nil:
		return "removed"
	}
	var parts []string
	if a, b := probeHandlerDesc(p), probeHandlerDesc(c); a != b {
		parts = append(parts, fmt.Sprintf("%s → %s", a, b))
	} else if p.Exec != nil && c.Exec != nil && !equalStrings(p.Exec.Command, c.Exec.Command) {
		parts = append(parts, "exec command changed")
	}
	fields := []struct {
		name string
		a, b int32
	}{
		{"initialDelaySeconds", p.InitialDelaySeconds, c.InitialDelaySeconds},
		{"timeoutSeconds", p.TimeoutSeconds, c.TimeoutSeconds},
		{"periodSeconds", p.PeriodSeconds, c.PeriodSeconds},
		{"successThreshold", p.SuccessThreshold, c.SuccessThreshold},
		{"failureThreshold", p.FailureThreshold, c.FailureThreshold},
	}
	for _, f := range fields {
		if f.a != f.b {
			parts = append(parts, fmt.Sprintf("%s %d → %d", f.name, f.a, f.b))
		}
	}
	if len(parts) == 0 {
		if equality.Semantic.DeepEqual(p, c) {
			return ""
		}
		return "changed"
	}
	return strings.Join(parts, ", ")
}

func probeHandlerDesc(p *corev1.Probe) string {
	switch {
	case p.HTTPGet != nil:
		scheme := ""
		if p.HTTPGet.Scheme != "" && p.HTTPGet.Scheme != corev1.URISchemeHTTP {
			scheme = string(p.HTTPGet.Scheme) + " "
		}
		// A query string can carry a token: only that there is one is said.
		path := p.HTTPGet.Path
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i] + "?…"
		}
		return fmt.Sprintf("httpGet %s%s:%s%s", scheme, revClip(p.HTTPGet.Host), revClip(p.HTTPGet.Port.String()), revClip(path))
	case p.TCPSocket != nil:
		return fmt.Sprintf("tcpSocket %s:%s", revClip(p.TCPSocket.Host), revClip(p.TCPSocket.Port.String()))
	case p.GRPC != nil:
		return fmt.Sprintf("grpc :%d", p.GRPC.Port)
	case p.Exec != nil:
		// The program only, as for a container's command.
		if prog := programName(p.Exec.Command); prog != "" {
			return "exec " + prog
		}
		return "exec"
	}
	return "no handler"
}

// volumeChanges diffs pod volumes by name; a volume's description is the
// ConfigMap / Secret / PVC it points at, or its kind for the others.
func volumeChanges(prev, cur []corev1.Volume) []string {
	prevByName := make(map[string]string, len(prev))
	for _, v := range prev {
		prevByName[v.Name] = volumeDesc(v)
	}
	inCur := make(map[string]bool, len(cur))
	var out []string
	for _, v := range cur {
		inCur[v.Name] = true
		d := volumeDesc(v)
		p, ok := prevByName[v.Name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("volume %s added (%s)", v.Name, d))
		case p != d:
			out = append(out, fmt.Sprintf("volume %s: %s → %s", v.Name, p, d))
		}
	}
	for _, v := range prev {
		if !inCur[v.Name] {
			out = append(out, fmt.Sprintf("volume %s removed", v.Name))
		}
	}
	return out
}

func volumeDesc(v corev1.Volume) string {
	s := v.VolumeSource
	switch {
	case s.ConfigMap != nil:
		return "configMap " + revClip(s.ConfigMap.Name)
	case s.Secret != nil:
		return "secret " + revClip(s.Secret.SecretName)
	case s.PersistentVolumeClaim != nil:
		return "pvc " + revClip(s.PersistentVolumeClaim.ClaimName)
	case s.Projected != nil:
		var refs []string
		for _, src := range s.Projected.Sources {
			switch {
			case src.ConfigMap != nil:
				refs = append(refs, "configMap "+src.ConfigMap.Name)
			case src.Secret != nil:
				refs = append(refs, "secret "+src.Secret.Name)
			}
		}
		if len(refs) == 0 {
			return "projected"
		}
		return "projected " + revClip(strings.Join(refs, ", "))
	case s.EmptyDir != nil:
		return "emptyDir"
	case s.HostPath != nil:
		return "hostPath " + revClip(s.HostPath.Path)
	case s.DownwardAPI != nil:
		return "downwardAPI"
	case s.CSI != nil:
		return "csi " + revClip(s.CSI.Driver)
	case s.Ephemeral != nil:
		return "ephemeral"
	}
	return "other volume source"
}

func serviceAccountOrDefault(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// changedKeys lists (sorted, clipped) the keys whose value differs between
// two maps, ignoring skip. Keys only: an annotation value can be anything.
func changedKeys(prev, cur map[string]string, skip string) string {
	var keys []string
	for k, v := range cur {
		if k == skip {
			continue
		}
		if pv, ok := prev[k]; !ok || pv != v {
			keys = append(keys, k)
		}
	}
	for k := range prev {
		if k == skip {
			continue
		}
		if _, ok := cur[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return revClip(strings.Join(keys, ", "))
}

// revClip is how a free-text value reaches a change line (an image, a probe's
// path, a ref name): without the runes that print nothing — controls, line
// breaks, format characters such as the Unicode tag block, which carries text
// a reader never sees — and clipped by clipMiddle. Env values are the
// exception: they are quoted, which escapes those runes instead.
func revClip(s string) string {
	return clipMiddle(strings.Map(func(r rune) rune {
		if !unicode.IsGraphic(r) {
			return -1
		}
		return r
	}, s))
}

// clipMiddle keeps a value at most maxChangeValueLen runes, eliding the
// MIDDLE: an image digest and a URL both differ at the end as often as at the
// start.
func clipMiddle(s string) string {
	r := []rune(s)
	if len(r) <= maxChangeValueLen {
		return s
	}
	head := maxChangeValueLen * 2 / 3
	tail := maxChangeValueLen - head - 1
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// revName renders a name read from the template (an env name, an envFrom
// prefix): clipped, and quoted when it holds anything but [A-Za-z0-9_.-] — the
// relaxed env-name validation admits spaces and quotes, and an unquoted name
// could forge the rest of its line.
func revName(s string) string {
	c := clipMiddle(s)
	if plainName.MatchString(c) {
		return c
	}
	return strconv.Quote(c)
}
