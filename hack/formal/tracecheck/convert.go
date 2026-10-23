package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"github.com/argoproj-labs/gitops-promoter/test/tracerecord"
)

// deleted is the watch event type of a deletion.
const deleted = "DELETED"

// record is one recorded change, decoded.
type record struct {
	obj  client.Object
	typ  string
	kind string
	rv   uint64
	seq  int
}

func (r record) describe() string {
	return fmt.Sprintf("%s %s %s/%s (resourceVersion %d)", r.typ, r.kind, r.obj.GetNamespace(), r.obj.GetName(), r.rv)
}

// readRecords decodes a file written by tracerecord.Recorder and orders it by resourceVersion,
// which an etcd-backed API server assigns from one counter for every write. It also reports the
// kinds whose watch lost history.
func readRecords(in io.Reader) ([]record, []string, error) {
	var records []record
	var gaps []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for seq := 0; sc.Scan(); seq++ {
		var ev tracerecord.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", seq+1, err)
		}
		if ev.Type == "GAP" {
			gaps = append(gaps, ev.Kind)
			continue
		}
		obj, err := decodeObject(ev.Kind, ev.Object)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", seq+1, err)
		}
		rv, err := strconv.ParseUint(obj.GetResourceVersion(), 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: resourceVersion %q is not a number", seq+1, obj.GetResourceVersion())
		}
		// A watch that starts over reports the current state again.
		key := fmt.Sprintf("%s/%s/%d/%s", ev.Kind, obj.GetUID(), rv, ev.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		records = append(records, record{rv: rv, seq: seq, typ: ev.Type, kind: ev.Kind, obj: obj})
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to read the recording: %w", err)
	}
	slices.SortStableFunc(records, func(a, b record) int { return cmp.Compare(a.rv, b.rv) })
	return records, gaps, nil
}

func decodeObject(kind string, data json.RawMessage) (client.Object, error) {
	var obj client.Object
	switch kind {
	case "PromotionStrategy":
		obj = &promoterv1alpha1.PromotionStrategy{}
	case "ChangeTransferPolicy":
		obj = &promoterv1alpha1.ChangeTransferPolicy{}
	case "PullRequest":
		obj = &promoterv1alpha1.PullRequest{}
	case "CommitStatus":
		obj = &promoterv1alpha1.CommitStatus{}
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fmt.Errorf("failed to decode %s: %w", kind, err)
	}
	return obj, nil
}

// rawView is an EnvView before SHAs are numbered.
type rawView struct {
	activeSha, activeDry, activeHealth              string
	proposedSha, proposedDry, noteDry, proposedGate string
}

type rawPullRequest struct {
	specState   string
	mergeSha    string
	scm         string
	exists      bool
	terminating bool
}

type eventKind int

const (
	ctpStatusEvent eventKind = iota
	psStatusEvent
	gateEvent
	pullRequestEvent
)

// traceEvent is one Event of tracecheck.qnt, before SHAs are numbered.
type traceEvent struct {
	views  map[string]rawView
	view   rawView
	env    string
	sha    string
	phase  string
	pr     rawPullRequest
	source record
	kind   eventKind
}

// trace is the history of one PromotionStrategy (one UID: a deleted and recreated
// PromotionStrategy with the same name is a new trace).
type trace struct {
	lastPs     map[string]rawView
	graph      map[string][]string
	lastPR     map[string]rawPullRequest
	dryTimes   map[string]time.Time
	lastGate   map[string][2]string
	lastCtp    map[string]rawView
	name       string
	uid        string
	namespace  string
	gateName   string
	envs       []string
	dryOrder   []string
	events     []traceEvent
	hadPsEvent bool
}

// resolveGraph mirrors resolveDependentEnvironments: explicit dependsOn if any environment has
// one, else a chain in spec.environments order.
func resolveGraph(ps *promoterv1alpha1.PromotionStrategy) map[string][]string {
	explicit := slices.ContainsFunc(ps.Spec.Environments, func(e promoterv1alpha1.Environment) bool {
		return len(e.DependsOn) > 0
	})
	g := map[string][]string{}
	for i, env := range ps.Spec.Environments {
		switch {
		case explicit:
			g[env.Branch] = slices.Clone(env.DependsOn)
		case i > 0:
			g[env.Branch] = []string{ps.Spec.Environments[i-1].Branch}
		default:
			g[env.Branch] = nil
		}
	}
	return g
}

func (t *trace) noteDry(sha string, at time.Time) {
	if sha == "" {
		return
	}
	if _, ok := t.dryTimes[sha]; ok {
		if !at.IsZero() && t.dryTimes[sha].IsZero() {
			t.dryTimes[sha] = at
		}
		return
	}
	t.dryTimes[sha] = at
	t.dryOrder = append(t.dryOrder, sha)
}

func passing(statuses []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase) string {
	if utils.AreCommitStatusesPassing(statuses) {
		return "success"
	}
	return "pending"
}

func (t *trace) view(active, proposed promoterv1alpha1.CommitBranchState) rawView {
	t.noteDry(active.Dry.Sha, active.Dry.CommitTime.Time)
	t.noteDry(proposed.Dry.Sha, proposed.Dry.CommitTime.Time)
	note := proposed.Dry.Sha
	if proposed.Note != nil && proposed.Note.DrySha != "" {
		note = proposed.Note.DrySha
	}
	t.noteDry(note, time.Time{})
	return rawView{
		activeSha:    active.Hydrated.Sha,
		activeDry:    active.Dry.Sha,
		activeHealth: passing(active.CommitStatuses),
		proposedSha:  proposed.Hydrated.Sha,
		proposedDry:  proposed.Dry.Sha,
		noteDry:      note,
		proposedGate: passing(proposed.CommitStatuses),
	}
}

func controllerOwner(obj client.Object, kind string) (name, uid string, ok bool) {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == kind {
			return ref.Name, string(ref.UID), true
		}
	}
	return "", "", false
}

type ctpRef struct {
	trace *trace
	env   string
}

// builder splits records into one trace per PromotionStrategy.
type builder struct {
	byUID  map[string]*trace
	byGate map[string]*trace // namespace/DependentsSuccessfulCommitStatus name -> live trace
	ctps   map[string]ctpRef // ChangeTransferPolicy UID -> trace and environment
	order  []*trace
}

func buildTraces(records []record) []*trace {
	b := &builder{byUID: map[string]*trace{}, byGate: map[string]*trace{}, ctps: map[string]ctpRef{}}
	for _, r := range records {
		switch obj := r.obj.(type) {
		case *promoterv1alpha1.PromotionStrategy:
			b.promotionStrategy(r, obj)
		case *promoterv1alpha1.ChangeTransferPolicy:
			b.changeTransferPolicy(r, obj)
		case *promoterv1alpha1.PullRequest:
			b.pullRequest(r, obj)
		case *promoterv1alpha1.CommitStatus:
			b.commitStatus(r, obj)
		default:
		}
	}
	return b.order
}

func (b *builder) promotionStrategy(r record, obj *promoterv1alpha1.PromotionStrategy) {
	t := b.byUID[string(obj.UID)]
	if t == nil {
		t = &trace{
			namespace: obj.Namespace, name: obj.Name, uid: string(obj.UID),
			dryTimes: map[string]time.Time{}, lastCtp: map[string]rawView{},
			lastGate: map[string][2]string{}, lastPR: map[string]rawPullRequest{},
		}
		b.byUID[t.uid] = t
		b.order = append(b.order, t)
	}
	t.envs = t.envs[:0]
	for _, env := range obj.Spec.Environments {
		t.envs = append(t.envs, env.Branch)
	}
	t.graph = resolveGraph(obj)
	t.gateName = obj.Spec.OrderCommitStatusRef.Name
	b.byGate[obj.Namespace+"/"+t.gateName] = t
	if r.typ == deleted {
		return
	}
	views := map[string]rawView{}
	for _, env := range obj.Status.Environments {
		if slices.Contains(t.envs, env.Branch) {
			views[env.Branch] = t.view(env.Active, env.Proposed)
		}
	}
	if !t.hadPsEvent || !mapsEqual(views, t.lastPs) {
		t.hadPsEvent = true
		t.lastPs = views
		t.events = append(t.events, traceEvent{kind: psStatusEvent, views: views, source: r})
	}
}

func (b *builder) changeTransferPolicy(r record, obj *promoterv1alpha1.ChangeTransferPolicy) {
	_, psUID, ok := controllerOwner(obj, "PromotionStrategy")
	t := b.byUID[psUID]
	if !ok || t == nil {
		return
	}
	env := obj.Spec.ActiveBranch
	b.ctps[string(obj.UID)] = ctpRef{trace: t, env: env}
	if r.typ == deleted {
		return
	}
	v := t.view(obj.Status.Active, obj.Status.Proposed)
	if last, ok := t.lastCtp[env]; !ok || last != v {
		t.lastCtp[env] = v
		t.events = append(t.events, traceEvent{kind: ctpStatusEvent, env: env, view: v, source: r})
	}
}

func (b *builder) pullRequest(r record, obj *promoterv1alpha1.PullRequest) {
	_, ctpUID, ok := controllerOwner(obj, "ChangeTransferPolicy")
	ref, known := b.ctps[ctpUID]
	if !ok || !known {
		return
	}
	p := rawPullRequest{}
	if r.typ != deleted {
		p = rawPullRequest{
			exists:      true,
			specState:   string(obj.Spec.State),
			mergeSha:    obj.Spec.MergeSha,
			scm:         string(obj.Status.State),
			terminating: obj.DeletionTimestamp != nil,
		}
		if p.scm == "" {
			p.scm = "none"
		}
	}
	t := ref.trace
	if last, ok := t.lastPR[ref.env]; ok && last == p {
		return
	}
	t.lastPR[ref.env] = p
	t.events = append(t.events, traceEvent{kind: pullRequestEvent, env: ref.env, pr: p, source: r})
}

func (b *builder) commitStatus(r record, obj *promoterv1alpha1.CommitStatus) {
	gateName, _, ok := controllerOwner(obj, "DependentsSuccessfulCommitStatus")
	t := b.byGate[obj.Namespace+"/"+gateName]
	if !ok || t == nil || r.typ == deleted {
		return
	}
	env := ""
	for _, branch := range t.envs {
		if utils.KubeSafeLabel(branch) == obj.Labels[promoterv1alpha1.EnvironmentLabel] {
			env = branch
		}
	}
	if env == "" {
		return
	}
	g := [2]string{obj.Spec.Sha, string(obj.Spec.Phase)}
	if t.lastGate[env] == g {
		return
	}
	t.lastGate[env] = g
	t.events = append(t.events, traceEvent{kind: gateEvent, env: env, sha: g[0], phase: g[1], source: r})
}

func mapsEqual(a, b map[string]rawView) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// numbering maps SHAs to the integers the model uses. Dry SHAs are numbered in commit-time order
// (first seen breaks ties, and a SHA seen without a time takes the latest time seen before it), so
// comparing numbers compares commit order. dryTime gives equal times equal ranks, as the code's
// time comparison does. Hydrated SHAs are numbered in the order they appear. "" is -1.
type numbering struct {
	dry      map[string]int
	dryTime  map[int]int
	hydrated map[string]int
}

func (t *trace) number() numbering {
	n := numbering{dry: map[string]int{}, dryTime: map[int]int{-1: -1}, hydrated: map[string]int{}}
	type entry struct {
		at    time.Time
		sha   string
		first int
	}
	entries := make([]entry, 0, len(t.dryOrder))
	var latest time.Time
	for i, sha := range t.dryOrder {
		at := t.dryTimes[sha]
		if at.IsZero() {
			at = latest
		} else if at.After(latest) {
			latest = at
		}
		entries = append(entries, entry{sha: sha, at: at, first: i})
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return cmp.Compare(a.first, b.first)
	})

	rank := -1
	for i, e := range entries {
		if i == 0 || !e.at.Equal(entries[i-1].at) {
			rank++
		}
		n.dry[e.sha] = i
		n.dryTime[i] = rank
	}
	return n
}

func (n numbering) dryID(sha string) int {
	if sha == "" {
		return -1
	}
	return n.dry[sha]
}

func (n numbering) hydratedID(sha string) int {
	if sha == "" {
		return -1
	}
	id, ok := n.hydrated[sha]
	if !ok {
		id = len(n.hydrated)
		n.hydrated[sha] = id
	}
	return id
}

func (n numbering) quintView(v rawView) string {
	return fmt.Sprintf("{ activeSha: %d, activeDry: %d, activeHealth: %q, "+
		"proposedSha: %d, proposedDry: %d, noteDry: %d, proposedGate: %q }",
		n.hydratedID(v.activeSha), n.dryID(v.activeDry), v.activeHealth,
		n.hydratedID(v.proposedSha), n.dryID(v.proposedDry), n.dryID(v.noteDry), v.proposedGate)
}

func (n numbering) quintEvent(ev traceEvent) string {
	switch ev.kind {
	case ctpStatusEvent:
		return fmt.Sprintf("CtpStatus({ env: %q, view: %s })", ev.env, n.quintView(ev.view))
	case psStatusEvent:
		envs := make([]string, 0, len(ev.views))
		for env := range ev.views {
			envs = append(envs, env)
		}
		slices.Sort(envs)
		entries := make([]string, 0, len(envs))
		for _, env := range envs {
			entries = append(entries, fmt.Sprintf("%q -> %s", env, n.quintView(ev.views[env])))
		}
		return fmt.Sprintf("PsStatus(Map(%s))", strings.Join(entries, ", "))
	case gateEvent:
		return fmt.Sprintf("Gate({ env: %q, cs: { sha: %d, phase: %q } })", ev.env, n.hydratedID(ev.sha), ev.phase)
	default:
		if !ev.pr.exists {
			return fmt.Sprintf("PullRequestChange({ env: %q, pr: NO_PULL_REQUEST })", ev.env)
		}
		return fmt.Sprintf("PullRequestChange({ env: %q, pr: { exists: true, specState: %q, mergeSha: %d, "+
			"scm: %q, terminating: %t } })",
			ev.env, ev.pr.specState, n.hydratedID(ev.pr.mergeSha), ev.pr.scm, ev.pr.terminating)
	}
}

// quintModule renders the trace as a module that checks it when initialized.
func (t *trace) quintModule(module, formalDir string) string {
	n := t.number()
	var b strings.Builder
	fmt.Fprintf(&b, "// Generated by hack/formal/tracecheck from PromotionStrategy %s/%s (uid %s).\n",
		t.namespace, t.name, t.uid)
	fmt.Fprintf(&b, "module %s {\n", module)
	fmt.Fprintf(&b, "  import logic.* from %q\n", formalDir+"/promoter")
	fmt.Fprintf(&b, "  import tracecheck.* from %q\n\n", formalDir+"/tracecheck")

	envs := slices.Clone(t.envs)
	slices.Sort(envs)
	graph := make([]string, 0, len(envs))
	for _, env := range envs {
		deps := make([]string, 0, len(t.graph[env]))
		for _, d := range t.graph[env] {
			deps = append(deps, strconv.Quote(d))
		}
		graph = append(graph, fmt.Sprintf("%q -> Set(%s)", env, strings.Join(deps, ", ")))
	}
	fmt.Fprintf(&b, "  pure val G: str -> Set[str] = Map(%s)\n", strings.Join(graph, ", "))

	ids := make([]int, 0, len(n.dryTime))
	for id := range n.dryTime {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	times := make([]string, 0, len(ids))
	for _, id := range ids {
		times = append(times, fmt.Sprintf("%d -> %d", id, n.dryTime[id]))
	}
	fmt.Fprintf(&b, "  pure val DT: int -> int = Map(%s)\n", strings.Join(times, ", "))

	b.WriteString("  pure val TRACE: List[Event] = [\n")
	for i, ev := range t.events {
		fmt.Fprintf(&b, "    %s, // %d: %s\n", n.quintEvent(ev), i, ev.source.describe())
	}
	b.WriteString("  ]\n\n")
	b.WriteString("  var violations: List[Violation]\n")
	b.WriteString("  action init = violations' = checkTrace(G, DT, TRACE)\n")
	b.WriteString("  action step = violations' = violations\n")
	b.WriteString("  val ok = violations == []\n")
	b.WriteString("}\n")
	return b.String()
}

// stats counts what a trace exercised, for the report.
type stats struct {
	gateWrites, specChanges, controllerMerges, externalMerges int
}

func (t *trace) stats() stats {
	var s stats
	last := map[string]rawPullRequest{}
	requested := map[string]map[string]bool{}
	for _, ev := range t.events {
		switch ev.kind {
		case gateEvent:
			s.gateWrites++
		case pullRequestEvent:
			prev := last[ev.env]
			p := ev.pr
			if p.exists && (!prev.exists || prev.specState != p.specState || prev.mergeSha != p.mergeSha) {
				s.specChanges++
			}
			if requested[ev.env] == nil || !p.exists {
				requested[ev.env] = map[string]bool{}
			}
			if p.exists && p.scm == "merged" && prev.scm != "merged" {
				if requested[ev.env][p.mergeSha] {
					s.controllerMerges++
				} else {
					s.externalMerges++
				}
			}
			if p.specState == string(promoterv1alpha1.PullRequestMerged) {
				requested[ev.env][p.mergeSha] = true
			}
			last[ev.env] = p
		default:
		}
	}
	return s
}
