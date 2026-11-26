package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/costmodel"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
	"github.com/kubebolt/kubebolt/apps/api/internal/rightsizing"
)

// Kobi Plans (plans-03): create_plan's source. This file reads the cluster,
// the metrics store, the org and the exchange rate; kobiplan.BuildRightsizing
// decides; copilot.PlanStore keeps the result.

type planStoreBox struct{ s copilot.PlanStore }

var planStorePtr atomic.Pointer[planStoreBox]

// SetKobiPlanStore installs the plan store (main.go). nil leaves Kobi
// without create_plan.
func SetKobiPlanStore(s copilot.PlanStore) { planStorePtr.Store(&planStoreBox{s}) }

func kobiPlanStore() copilot.PlanStore {
	if b := planStorePtr.Load(); b != nil {
		return b.s
	}
	return nil
}

// planFX converts OpenCost's prices into the org's currency (P9). A
// variable so tests can give it a fixed rate.
var planFX costmodel.FX = costmodel.NewECB()

// openCostCurrency is the currency OpenCost prices in. OpenCost's default;
// a per-cluster setting for clusters configured otherwise does not exist yet.
const openCostCurrency = "usd"

// Bounds on the field-manager reads (one metadata GET per candidate).
const (
	planManagerReads       = 50
	planManagerConcurrency = 4
	planManagerTimeout     = 3 * time.Second
)

// planSourceFor is create_plan's source for one chat turn: the person and
// the conversation every plan it creates belongs to. nil without a store.
func (h *handlers) planSourceFor(tenant, user, userName, conversationID string) copilot.PlanSource {
	store := kobiPlanStore()
	if store == nil || h.manager == nil {
		return nil
	}
	return planSource{h: h, store: store, tenant: tenant, user: user, userName: userName, conversation: conversationID}
}

// requestUserName is the signed-in person's username, empty without auth.
func requestUserName(r *http.Request) string {
	if c := auth.ContextClaims(r); c != nil {
		return c.Username
	}
	return ""
}

type planSource struct {
	h            *handlers
	store        copilot.PlanStore
	tenant       string
	user         string
	userName     string
	conversation string
}

var errNoClusterID = errors.New("the cluster's id is not known yet, so its metrics cannot be read")

// errConsolidationNotOnPlan: consolidation plans are for paid plans (26-nov,
// the user's decision; which tier first is decided while it is a beta).
var errConsolidationNotOnPlan = errors.New("consolidation plans come with the paid plans, and this organization is on Free; right-sizing plans are available")

func (s planSource) CreatePlan(ctx context.Context, req copilot.CreatePlanRequest) (copilot.CreatePlanOutcome, error) {
	// First: on Free, a consolidation request is answered by what the plan
	// allows, not by Free's one live plan coming back.
	if req.Kind == kobiplan.KindConsolidation && !s.h.consolidationOnPlan(ctx) {
		if !consolidationEdition {
			return copilot.CreatePlanOutcome{}, errConsolidationNotInEdition
		}
		return copilot.CreatePlanOutcome{}, errConsolidationNotOnPlan
	}
	conn := s.h.manager.Connector(ctx)
	if conn == nil {
		return copilot.CreatePlanOutcome{}, errClusterNotConnected
	}
	clusterID := s.h.activeClusterUID(ctx)
	if clusterID == "" {
		return copilot.CreatePlanOutcome{}, errNoClusterID
	}
	now := time.Now().UTC()

	live, err := s.store.LiveForCluster(s.tenant, clusterID)
	if err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	kind := req.Kind
	if kind == "" {
		kind = kobiplan.KindRightsizing
	}
	scope := kobiplan.Scope{Namespaces: req.Namespaces, Workloads: req.Workloads}
	if kind == kobiplan.KindConsolidation {
		// A consolidation plan reads every pool: a node is shared by every
		// namespace it runs.
		scope = kobiplan.Scope{}
	}
	existing, why, planned := liveAnswer(live, kind, scope, s.h.oneLivePlanPerCluster(ctx), now)
	if existing != nil {
		return copilot.CreatePlanOutcome{Existing: existing, ExistingReason: why}, nil
	}
	if kind == kobiplan.KindConsolidation {
		return s.createConsolidation(ctx, conn, clusterID, live, req, now)
	}

	res, computedAt, err := s.h.computeRightSizingAt(ctx, conn)
	if err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	if res.InsufficientHistory {
		return copilot.CreatePlanOutcome{}, fmt.Errorf("%w (it needs %d hours of usage history)", kobiplan.ErrInsufficientHistory, res.MinHistoryHours)
	}
	workloads, notes, err := s.h.planWorkloadFacts(ctx, conn, clusterID, res, scope)
	if err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	pricing, priceNotes := s.h.planPricing(ctx, clusterID)

	p, err := kobiplan.BuildRightsizing(kobiplan.RightsizingInput{
		ID:         "pln_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		OrgID:      s.tenant,
		ClusterID:  clusterID,
		Title:      planTitle(req),
		Source:     kobiplan.Source{Initiator: kobiplan.InitiatorChat, UserID: s.user, UserName: s.userName, ConversationID: s.conversation},
		Scope:      scope,
		Result:     res,
		ComputedAt: computedAt,
		Workloads:  workloads,
		Pricing:    pricing,
		Planned:    planned,
		Notes:      append(notes, priceNotes...),
		Now:        now,
	})
	if err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	if len(p.Steps) == 0 {
		return copilot.CreatePlanOutcome{Empty: &p}, nil
	}
	if err := s.store.Create(p); err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	slog.Info("kobi plan created",
		slog.String("plan", p.ID), slog.String("cluster", clusterID),
		slog.Int("steps", len(p.Steps)), slog.Int("excluded", len(p.Excluded)))
	return copilot.CreatePlanOutcome{Created: &p}, nil
}

// liveAnswer reads the cluster's live plans before a new one is built.
//   - Free keeps one live plan per cluster (P21): that one comes back, while
//     it has a step to do.
//   - The same request again comes back to the plan that answers it while
//     something in it is still to do (plans-06): each conversation asking
//     the same thing used to add a draft.
//   - Otherwise planned names the workloads with a pending step in another
//     live plan, which the new plan leaves out so no saving counts twice.
//
// A draft past its expiry is no longer live, whether or not the sweep has
// marked it yet.
func liveAnswer(live []kobiplan.Plan, kind kobiplan.Kind, scope kobiplan.Scope, onePerCluster bool, now time.Time) (*kobiplan.Plan, copilot.ExistingReason, map[string]string) {
	live = slices.DeleteFunc(slices.Clone(live), func(p kobiplan.Plan) bool {
		return p.Status == kobiplan.StatusDraft && !p.ExpiresAt.After(now)
	})
	if onePerCluster {
		// A plan with every step applied is done: it does not hold the place.
		for i := range live {
			if live[i].HasPendingStep() {
				return &live[i], copilot.ExistingOnePerCluster, nil
			}
		}
	}
	planned := map[string]string{}
	for i, p := range live {
		if p.Kind == kind && kobiplan.SameScope(p.Scope, scope) && p.HasPendingStep() {
			return &live[i], copilot.ExistingSameScope, nil
		}
		for _, st := range p.Steps {
			if st.Status == kobiplan.StepPending {
				planned[kobiplan.TargetKey(st.Target.Type, st.Target.Namespace, st.Target.Name)] = p.Title
			}
		}
	}
	return nil, "", planned
}

// oneLivePlanPerCluster: the Free plan's limit (P21). Only a multi-tenant
// install sells plans; a self-hosted one has no such limit.
func (h *handlers) oneLivePlanPerCluster(ctx context.Context) bool {
	return auth.MultiTenantEnabled && auth.IsFreePlan(h.orgPlan(ctx))
}

// consolidationOnPlan: consolidation plans come with the editions that have
// them (consolidationEdition), and there with the paid plans. Only a
// multi-tenant install sells plans; a self-hosted Enterprise one has them all.
func (h *handlers) consolidationOnPlan(ctx context.Context) bool {
	return consolidationEdition && !(auth.MultiTenantEnabled && auth.IsFreePlan(h.orgPlan(ctx)))
}

func planTitle(req copilot.CreatePlanRequest) string {
	if req.Title != "" {
		return req.Title
	}
	switch {
	case len(req.Namespaces) > 0:
		return "Right-sizing: " + strings.Join(req.Namespaces, ", ")
	case len(req.Workloads) > 0:
		return "Right-sizing: " + strings.Join(req.Workloads, ", ")
	}
	return "Right-sizing: whole cluster"
}

// planWorkloadFacts reads, for every workload the engine flagged in scope,
// what the builder decides with: the pod template, its metadata and field
// managers, the pods and whether their count held, and the HPA that scales
// it.
func (h *handlers) planWorkloadFacts(ctx context.Context, conn *cluster.Connector, clusterID string, res rightsizing.Result, scope kobiplan.Scope) (map[string]kobiplan.WorkloadFacts, []string, error) {
	var notes []string
	out := map[string]kobiplan.WorkloadFacts{}

	facts := map[string]kobiplan.WorkloadFacts{}
	// The pod template's labels, for matching a webhook's Service to the
	// workload whose pods it selects (plans-13).
	podLabels := map[string]map[string]string{}
	for _, d := range conn.GetDeployments() {
		podLabels[rightsizing.Key(d.Namespace, "Deployment", d.Name)] = d.Spec.Template.Labels
		facts[rightsizing.Key(d.Namespace, "Deployment", d.Name)] = kobiplan.WorkloadFacts{
			Type: "deployments", Namespace: d.Namespace, Name: d.Name, Generation: d.Generation,
			Template: d.Spec.Template.Spec, Object: objectFacts(d.Labels, d.Annotations, d.OwnerReferences),
		}
	}
	for _, s := range conn.GetStatefulSets() {
		podLabels[rightsizing.Key(s.Namespace, "StatefulSet", s.Name)] = s.Spec.Template.Labels
		facts[rightsizing.Key(s.Namespace, "StatefulSet", s.Name)] = kobiplan.WorkloadFacts{
			Type: "statefulsets", Namespace: s.Namespace, Name: s.Name, Generation: s.Generation,
			Template: s.Spec.Template.Spec, Object: objectFacts(s.Labels, s.Annotations, s.OwnerReferences),
		}
	}
	for _, d := range conn.GetDaemonSets() {
		podLabels[rightsizing.Key(d.Namespace, "DaemonSet", d.Name)] = d.Spec.Template.Labels
		facts[rightsizing.Key(d.Namespace, "DaemonSet", d.Name)] = kobiplan.WorkloadFacts{
			Type: "daemonsets", Namespace: d.Namespace, Name: d.Name, Generation: d.Generation,
			Template: d.Spec.Template.Spec, Object: objectFacts(d.Labels, d.Annotations, d.OwnerReferences),
		}
	}

	// The pods the engine summed, from the same cache it read.
	pods := map[string]int{}
	for _, nsw := range conn.NamespaceWorkloads() {
		for _, w := range nsw.Workloads {
			pods[rightsizing.Key(w.Namespace, w.Kind, w.Name)] = len(w.Pods)
		}
	}

	hpas := map[string]string{}
	for _, a := range conn.GetHPAs() {
		hpas[rightsizing.Key(a.Namespace, a.Spec.ScaleTargetRef.Kind, a.Spec.ScaleTargetRef.Name)] = a.Name
	}
	if !conn.Permissions().CanListWatch("hpas") {
		notes = append(notes, "HorizontalPodAutoscalers could not be read on this cluster, so a step may touch a workload an HPA scales: check before exporting.")
	}

	var keys []string
	for _, rec := range res.Recs {
		k := rightsizing.Key(rec.Namespace, rec.Kind, rec.Name)
		f, ok := facts[k]
		if !ok || !kobiplan.InScope(scope, rec.Namespace, rec.Kind, rec.Name) {
			continue
		}
		f.Pods = pods[k]
		f.HPA = hpas[k]
		out[k] = f
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return out, notes, nil
	}

	stable, err := h.planPodStability(ctx, clusterID)
	if err != nil {
		return nil, nil, err
	}
	for _, k := range keys {
		f := out[k]
		f.PodsStable = stable.stable(k, f.Pods)
		out[k] = f
	}

	h.readFieldManagers(ctx, conn, out, keys)
	if !readAdmissionWebhooks(ctx, conn, out, keys, podLabels) {
		notes = append(notes, "Admission webhooks could not be read on this cluster, so a step may resize a webhook's server without saying so: check before exporting.")
	}
	return out, notes, nil
}

// readAdmissionWebhooks names, for each candidate workload, the admission
// webhooks its pods serve: a webhook's Service selects them (plans-13). One
// LIST of each kind of configuration per plan. False when they could not be
// read.
func readAdmissionWebhooks(ctx context.Context, conn *cluster.Connector, facts map[string]kobiplan.WorkloadFacts, keys []string, podLabels map[string]map[string]string) bool {
	cs := conn.Clientset()
	if cs == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, planManagerTimeout)
	defer cancel()
	type hook struct {
		name, svcNS, svcName string
		facts                kobiplan.WebhookFacts
	}
	var hooks []hook
	add := func(name string, cc admissionregistrationv1.WebhookClientConfig, policy *admissionregistrationv1.FailurePolicyType, timeout *int32) {
		if cc.Service == nil {
			return // a URL webhook runs outside the cluster
		}
		// The API's defaults: Fail and 10 seconds.
		f := kobiplan.WebhookFacts{Name: name, FailurePolicy: string(admissionregistrationv1.Fail), TimeoutSeconds: 10}
		if policy != nil {
			f.FailurePolicy = string(*policy)
		}
		if timeout != nil {
			f.TimeoutSeconds = *timeout
		}
		hooks = append(hooks, hook{name, cc.Service.Namespace, cc.Service.Name, f})
	}
	validating, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for _, c := range validating.Items {
		for _, w := range c.Webhooks {
			add(w.Name, w.ClientConfig, w.FailurePolicy, w.TimeoutSeconds)
		}
	}
	mutating, err := cs.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for _, c := range mutating.Items {
		for _, w := range c.Webhooks {
			add(w.Name, w.ClientConfig, w.FailurePolicy, w.TimeoutSeconds)
		}
	}
	if len(hooks) == 0 {
		return true
	}
	selectors := map[string]labels.Selector{}
	for _, svc := range conn.GetServices() {
		if len(svc.Spec.Selector) > 0 {
			selectors[svc.Namespace+"/"+svc.Name] = labels.SelectorFromSet(svc.Spec.Selector)
		}
	}
	for _, k := range keys {
		f := facts[k]
		for _, hk := range hooks {
			sel, ok := selectors[hk.svcNS+"/"+hk.svcName]
			if ok && hk.svcNS == f.Namespace && sel.Matches(labels.Set(podLabels[k])) {
				f.Webhooks = append(f.Webhooks, hk.facts)
			}
		}
		facts[k] = f
	}
	return true
}

func objectFacts(labels, annotations map[string]string, refs []metav1.OwnerReference) kobiplan.ObjectFacts {
	return kobiplan.ObjectFacts{Labels: labels, Annotations: annotations, OwnerRefs: refs}
}

// readFieldManagers reads the field managers of the workloads that could
// become a step, so an EKS add-on without a label is still recognised. Only
// those: the ones a label already names as the provider's or an operator's
// are excluded anyway. A failed read leaves the labels to decide.
func (h *handlers) readFieldManagers(ctx context.Context, conn *cluster.Connector, facts map[string]kobiplan.WorkloadFacts, keys []string) {
	var todo []string
	for _, k := range keys {
		owner, _ := kobiplan.DetectOwner(facts[k].Object)
		if owner.Kind == kobiplan.OwnerProvider || owner.Kind == kobiplan.OwnerOperator {
			continue
		}
		todo = append(todo, k)
		if len(todo) == planManagerReads {
			break
		}
	}
	// Results go to their own map and are merged after the reads: the
	// goroutines never touch facts.
	read := make(map[string][]string, len(todo))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, planManagerConcurrency)
	for _, k := range todo {
		f := facts[k]
		wg.Add(1)
		sem <- struct{}{}
		go func(k string, typ, namespace, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			rctx, cancel := context.WithTimeout(ctx, planManagerTimeout)
			defer cancel()
			managers, err := conn.FieldManagers(rctx, appsv1.SchemeGroupVersion.WithResource(typ), namespace, name)
			if err != nil {
				return
			}
			mu.Lock()
			read[k] = managers
			mu.Unlock()
		}(k, f.Type, f.Namespace, f.Name)
	}
	wg.Wait()
	for k, managers := range read {
		f := facts[k]
		f.Object.Managers = managers
		facts[k] = f
	}
}

// Pod stability: the number of pods each workload ran, at the 5th and the
// 95th percentile of the right-sizing window, collapsed like the P95. Equal
// to each other and to today's count means the count held, and the engine's
// total divided by it is a per-pod value; the percentiles let a rollout's
// surge or a brief dip through.
func rightSizingPodsQuery(phi string) string {
	collapse := `label_replace(label_replace(container_cpu_usage_seconds_total{workload_kind="ReplicaSet",workload_name!=""}, ` +
		`"workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$"), ` +
		`"workload_kind", "Deployment", "workload_kind", "ReplicaSet") ` +
		`or container_cpu_usage_seconds_total{workload_kind=~"StatefulSet|DaemonSet",workload_name!=""}`
	return `quantile_over_time(` + phi + `, ` +
		`count by (workload_kind, workload_name, namespace) (` +
		`count by (workload_kind, workload_name, namespace, pod) (` + collapse + `)` +
		`)[7d:5m])`
}

type podStability struct{ p05, p95 map[string]int64 }

func (s podStability) stable(key string, pods int) bool {
	lo, okLo := s.p05[key]
	hi, okHi := s.p95[key]
	return okLo && okHi && pods > 0 && lo == hi && hi == int64(pods)
}

func (h *handlers) planPodStability(ctx context.Context, clusterID string) (podStability, error) {
	scope := func(q string) string {
		return scopeQueryByTenant(scopeQueryByCluster(q, clusterID), metricsTenantPinCtx(ctx))
	}
	lo, err := runInstantQuery(ctx, scope(rightSizingPodsQuery("0.05")))
	if err != nil {
		return podStability{}, err
	}
	hi, err := runInstantQuery(ctx, scope(rightSizingPodsQuery("0.95")))
	if err != nil {
		return podStability{}, err
	}
	return podStability{p05: roundIndex(lo), p95: roundIndex(hi)}, nil
}

func roundIndex(rows []vmRow) map[string]int64 {
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		ns, kind, name := r.Labels["namespace"], r.Labels["workload_kind"], r.Labels["workload_name"]
		if ns == "" || kind == "" || name == "" || math.IsNaN(r.Value) {
			continue
		}
		out[rightsizing.Key(ns, kind, name)] = int64(math.Round(r.Value))
	}
	return out
}

// planPricing reads the cluster's average node prices from OpenCost and the
// rate into the org's currency. Without prices there is no money; without
// a rate the money stays in OpenCost's currency and a note says so.
func (h *handlers) planPricing(ctx context.Context, clusterID string) (kobiplan.Pricing, []string) {
	scope := func(q string) string {
		return scopeQueryByTenant(scopeQueryByCluster(q, clusterID), metricsTenantPinCtx(ctx))
	}
	rate := func(q string) float64 {
		rows, err := runInstantQuery(ctx, scope(q))
		if err != nil || len(rows) == 0 || math.IsNaN(rows[0].Value) {
			return 0
		}
		return rows[0].Value
	}
	pricing := kobiplan.Pricing{
		Rates: costmodel.NodeRates{
			CPUCoreHourly: rate(`avg(node_cpu_hourly_cost)`),
			RAMGiBHourly:  rate(`avg(node_ram_hourly_cost)`),
		},
		Factor: 1, Currency: openCostCurrency, Source: "opencost",
	}
	if !pricing.Rates.Available() {
		return pricing, nil
	}
	want := planCurrency(h, ctx)
	if want == "" || want == openCostCurrency {
		return pricing, nil
	}
	if r, ok := planFX.Rate(ctx, openCostCurrency, want); ok {
		pricing.Factor, pricing.Currency = r, want
		return pricing, nil
	}
	return pricing, []string{fmt.Sprintf("Amounts are in %s, OpenCost's currency: no exchange rate to %s was available.",
		strings.ToUpper(openCostCurrency), strings.ToUpper(want))}
}
