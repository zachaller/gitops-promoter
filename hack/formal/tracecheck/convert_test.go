package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"github.com/argoproj-labs/gitops-promoter/test/tracerecord"
)

func recording(t *testing.T, events ...tracerecord.Event) []record {
	t.Helper()
	var buf bytes.Buffer
	for _, ev := range events {
		if err := json.NewEncoder(&buf).Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	records, gaps, err := readRecords(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps %v", gaps)
	}
	return records
}

func added(t *testing.T, kind string, obj client.Object, rv string) tracerecord.Event {
	t.Helper()
	obj.SetResourceVersion(rv)
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return tracerecord.Event{Type: "ADDED", Kind: kind, Object: data}
}

func owner(kind, name, uid string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, UID: types.UID(uid), Controller: new(true)}}
}

func branchState(hydrated, dry string, at time.Time, statuses ...string) promoterv1alpha1.CommitBranchState {
	s := promoterv1alpha1.CommitBranchState{
		Hydrated: promoterv1alpha1.CommitShaState{Sha: hydrated},
		Dry:      promoterv1alpha1.CommitShaState{Sha: dry, CommitTime: metav1.NewTime(at)},
	}
	for _, phase := range statuses {
		s.CommitStatuses = append(s.CommitStatuses,
			promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{Key: "k", Phase: phase})
	}
	return s
}

func TestBuildTraces(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ps := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{Name: "ps", Namespace: "ns", UID: "ps-uid"},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			OrderCommitStatusRef: promoterv1alpha1.OrderCommitStatusRef{Name: "gate"},
			Environments:         []promoterv1alpha1.Environment{{Branch: "dev"}, {Branch: "stg"}},
		},
	}
	ctp := &promoterv1alpha1.ChangeTransferPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ctp-stg", Namespace: "ns", UID: "ctp-uid", OwnerReferences: owner("PromotionStrategy", "ps", "ps-uid"),
		},
		Spec: promoterv1alpha1.ChangeTransferPolicySpec{ActiveBranch: "stg"},
		Status: promoterv1alpha1.ChangeTransferPolicyStatus{
			Active:   branchState("h-active", "dry-b", t0, "success"),
			Proposed: branchState("h-proposed", "dry-a", t0.Add(time.Minute), "pending"),
		},
	}
	pr := &promoterv1alpha1.PullRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pr", Namespace: "ns", UID: "pr-uid", OwnerReferences: owner("ChangeTransferPolicy", "ctp-stg", "ctp-uid"),
		},
		Spec: promoterv1alpha1.PullRequestSpec{State: promoterv1alpha1.PullRequestOpen, MergeSha: "h-proposed"},
	}
	gate := &promoterv1alpha1.CommitStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cs", Namespace: "ns", UID: "cs-uid",
			OwnerReferences: owner("DependentsSuccessfulCommitStatus", "gate", "dscs-uid"),
			Labels:          map[string]string{promoterv1alpha1.EnvironmentLabel: utils.KubeSafeLabel("stg")},
		},
		Spec: promoterv1alpha1.CommitStatusSpec{Sha: "h-proposed", Phase: promoterv1alpha1.CommitPhasePending},
	}

	// Recorded out of order, as separate watches deliver them, and the PullRequest twice.
	records := recording(t,
		added(t, "PullRequest", pr, "4"),
		added(t, "PromotionStrategy", ps, "1"),
		added(t, "CommitStatus", gate, "5"),
		added(t, "ChangeTransferPolicy", ctp, "3"),
		added(t, "PullRequest", pr, "4"),
	)
	traces := buildTraces(records)
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	got := traces[0].quintModule("m", "../formal")
	for _, want := range []string{
		`pure val G: str -> Set[str] = Map("dev" -> Set(), "stg" -> Set("dev"))`,
		`pure val DT: int -> int = Map(-1 -> -1, 0 -> 0, 1 -> 1)`,
		`PsStatus(Map()), // 0: ADDED PromotionStrategy ns/ps (resourceVersion 1)`,
		`CtpStatus({ env: "stg", view: { activeSha: 0, activeDry: 0, activeHealth: "success", ` +
			`proposedSha: 1, proposedDry: 1, noteDry: 1, proposedGate: "pending" } }), // 1:`,
		`PullRequestChange({ env: "stg", pr: { exists: true, specState: "open", mergeSha: 1, ` +
			`scm: "none", terminating: false } }), // 2:`,
		`Gate({ env: "stg", cs: { sha: 1, phase: "pending" } }), // 3:`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("module lacks %s\n%s", want, got)
		}
	}
	if strings.Count(got, "PullRequestChange") != 1 {
		t.Errorf("the repeated PullRequest event should be dropped\n%s", got)
	}
}

func TestNumberingGivesEqualTimesEqualRanks(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := &trace{dryTimes: map[string]time.Time{}}
	tr.noteDry("a", t0)
	tr.noteDry("b", t0)
	tr.noteDry("note-only", time.Time{})
	tr.noteDry("c", t0.Add(time.Second))
	n := tr.number()
	for sha, want := range map[string]int{"a": 0, "b": 1, "note-only": 2, "c": 3} {
		if n.dry[sha] != want {
			t.Errorf("dry %s numbered %d, want %d", sha, n.dry[sha], want)
		}
	}
	// a, b and the note-only SHA (which takes the latest time seen before it) share a time.
	for id, want := range map[int]int{0: 0, 1: 0, 2: 0, 3: 1} {
		if n.dryTime[id] != want {
			t.Errorf("dry %d has time rank %d, want %d", id, n.dryTime[id], want)
		}
	}
}
