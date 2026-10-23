// Command tracecheck checks runs of the controllers recorded by test/tracerecord against the
// Quint model in hack/formal: it splits each recording into one trace per PromotionStrategy,
// renders each trace as a Quint module that imports hack/formal/tracecheck.qnt, and runs Quint on
// it. See hack/formal/README.md.
//
//	go run ./hack/formal/tracecheck [-formal hack/formal] [-out DIR] RECORDING_OR_DIR...
//
// QUINT overrides the Quint command (default: npx --yes @informalsystems/quint@0.33.0).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func main() {
	formal := flag.String("formal", "hack/formal", "directory holding promoter.qnt and tracecheck.qnt")
	out := flag.String("out", "",
		"directory for the generated Quint modules (default: a quint directory next to the first recording)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: tracecheck [-formal DIR] [-out DIR] RECORDING_OR_DIR...")
		os.Exit(2)
	}
	ok, err := run(*formal, *out, flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracecheck:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func recordings(args []string) ([]string, error) {
	var files []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", arg, err)
		}
		if !info.IsDir() {
			files = append(files, arg)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(arg, "*.jsonl"))
		if err != nil {
			return nil, fmt.Errorf("failed to list %s: %w", arg, err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		return nil, errors.New("no recordings (*.jsonl) found")
	}
	return files, nil
}

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9]+`)

func run(formal, out string, args []string) (bool, error) {
	files, err := recordings(args)
	if err != nil {
		return false, err
	}
	if out == "" {
		out = filepath.Join(filepath.Dir(files[0]), "quint")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return false, fmt.Errorf("failed to create %s: %w", out, err)
	}
	formalRel, err := relativePath(out, formal)
	if err != nil {
		return false, err
	}

	allOK := true
	checked := 0
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return false, fmt.Errorf("failed to open %s: %w", file, err)
		}
		records, gaps, err := readRecords(f)
		_ = f.Close()
		if err != nil {
			return false, fmt.Errorf("%s: %w", file, err)
		}
		if len(gaps) > 0 {
			fmt.Printf("%s: the watch lost history for %s; changes in between are missing\n", file, strings.Join(gaps, ", "))
		}
		for _, t := range buildTraces(records) {
			if len(t.events) == 0 {
				continue
			}
			checked++
			module := fmt.Sprintf("trace%d_%s", checked, nonIdent.ReplaceAllString(t.name, "_"))
			path := filepath.Join(out, module+".qnt")
			if err := os.WriteFile(path, []byte(t.quintModule(module, filepath.ToSlash(formalRel))), 0o644); err != nil {
				return false, fmt.Errorf("failed to write %s: %w", path, err)
			}
			ok, err := check(t, module, path)
			if err != nil {
				return false, fmt.Errorf("%s: %w", path, err)
			}
			allOK = allOK && ok
		}
	}
	if checked == 0 {
		return false, errors.New("the recordings hold no PromotionStrategy with recorded changes")
	}
	return allOK, nil
}

// relativePath is target relative to dir, for a Quint import in a module written to dir.
func relativePath(dir, target string) (string, error) {
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %s: %w", dir, err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %s: %w", target, err)
	}
	rel, err := filepath.Rel(dirAbs, targetAbs)
	if err != nil {
		return "", fmt.Errorf("failed to relate %s to %s: %w", target, dir, err)
	}
	return rel, nil
}

type violation struct {
	Rule string `json:"rule"`
	Env  string `json:"env"`
	At   int    `json:"at"`
}

var ruleHelp = map[string]string{
	"gate": "the gate CommitStatus is not what gateFor gives for any PromotionStrategy status " +
		"stored before it",
	"pullRequestSpec": "the PullRequest spec change is neither upsertPullRequest nor requestMerge of " +
		"that reconcile's ChangeTransferPolicy status",
	"mergeOfUnknownCommit": "the SCM merged a SHA no ChangeTransferPolicy or PromotionStrategy status " +
		"reported as proposed",
	"ungated": "the SCM merged a commit whose dry SHA the gate never approved in this environment",
	"outOfOrder": "the SCM merged a dry SHA before an upstream had it active and healthy or passed it " +
		"through as a no-op",
	"rollback": "the SCM merged a dry SHA older than the environment's active one",
}

func check(t *trace, module, path string) (bool, error) {
	quint := strings.Fields(os.Getenv("QUINT"))
	if len(quint) == 0 {
		quint = []string{"npx", "--yes", "@informalsystems/quint@0.33.0"}
	}
	itf := strings.TrimSuffix(path, ".qnt") + ".itf.json"
	_ = os.Remove(itf)
	args := slices.Concat(quint[1:], []string{
		"run", path, "--main=" + module, "--max-steps=0", "--max-samples=1",
		"--invariant=ok", "--out-itf=" + itf, "--verbosity=0",
	})
	output, runErr := exec.CommandContext(context.Background(), quint[0], args...).CombinedOutput()

	s := t.stats()
	fmt.Printf("PromotionStrategy %s/%s (uid %s): %d events, %d gate writes, %d PullRequest spec changes, "+
		"%d merges the controller requested, %d merged by hand (not checked)\n",
		t.namespace, t.name, t.uid, len(t.events), s.gateWrites, s.specChanges, s.controllerMerges, s.externalMerges)

	violations, err := readViolations(itf)
	if err != nil {
		if runErr != nil {
			return false, fmt.Errorf("quint failed: %w\n%s", runErr, output)
		}
		return false, err
	}
	if len(violations) == 0 {
		if runErr != nil {
			return false, fmt.Errorf("quint failed: %w\n%s", runErr, output)
		}
		fmt.Println("  ok")
		return true, nil
	}
	for _, v := range violations {
		fmt.Printf("  %s in %s at event %d: %s\n", v.Rule, v.Env, v.At, ruleHelp[v.Rule])
		if v.At >= 0 && v.At < len(t.events) {
			fmt.Printf("    %s\n", t.events[v.At].source.describe())
		}
	}
	fmt.Printf("  the trace is %s\n", path)
	return false, nil
}

// readViolations reads the violations variable from the state Quint wrote to the ITF file.
func readViolations(path string) ([]violation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("quint wrote no trace: %w", err)
	}
	var itf struct {
		States []map[string]json.RawMessage `json:"states"`
	}
	if err := json.Unmarshal(data, &itf); err != nil {
		return nil, fmt.Errorf("failed to decode %s: %w", path, err)
	}
	if len(itf.States) == 0 {
		return nil, errors.New("quint wrote an empty trace")
	}
	var raw []struct {
		Rule string          `json:"rule"`
		Env  string          `json:"env"`
		At   json.RawMessage `json:"at"`
	}
	for key, value := range itf.States[len(itf.States)-1] {
		if strings.HasSuffix(key, "violations") {
			if err := json.Unmarshal(value, &raw); err != nil {
				return nil, fmt.Errorf("failed to decode the violations in %s: %w", path, err)
			}
		}
	}
	violations := make([]violation, 0, len(raw))
	for _, r := range raw {
		at, err := itfInt(r.At)
		if err != nil {
			return nil, fmt.Errorf("failed to decode a violation's index in %s: %w", path, err)
		}
		violations = append(violations, violation{At: at, Rule: r.Rule, Env: r.Env})
	}
	return violations, nil
}

func itfInt(raw json.RawMessage) (int, error) {
	var big struct {
		BigInt string `json:"#bigint"`
	}
	if err := json.Unmarshal(raw, &big); err == nil && big.BigInt != "" {
		n, err := strconv.Atoi(big.BigInt)
		if err != nil {
			return 0, fmt.Errorf("failed to parse %q: %w", big.BigInt, err)
		}
		return n, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("failed to parse %s: %w", raw, err)
	}
	return n, nil
}
