package loadtest

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const argoCDNamespace = "argocd"

// appsPath is the dry-branch path holding the placeholder workload manifest synced by the
// per-environment Applications in ModeArgoCD.
const appsPath = "apps"

// pathKey is the Application source/sourceHydrator "path" field name, factored out since it's
// repeated across drySource/syncSource/source.
const pathKey = "path"

// placeholderWorkloadManifest is the trivial workload synced by each per-environment
// Application in ModeArgoCD - just enough for Argo CD to report real health, without pulling
// in a full chart like cmd/demo's embedded helm-guestbook (kept small since --count can stamp
// out many instances).
const placeholderWorkloadManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: loadtest-placeholder
spec:
  replicas: 1
  selector:
    matchLabels:
      app: loadtest-placeholder
  template:
    metadata:
      labels:
        app: loadtest-placeholder
    spec:
      containers:
        - name: placeholder
          image: registry.k8s.io/pause:3.9
          resources:
            requests:
              cpu: 5m
              memory: 8Mi
`

// applicationGVK is the Argo CD Application GroupVersionKind. We build these as
// unstructured.Unstructured (rather than the trimmed-down internal/types/argocd.Application,
// which only has the fields the ArgoCDCommitStatus controller reads) so every field a real
// Application requires - destination, project, syncPolicy - can be set.
func newApplication(name string, spec map[string]any, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("argoproj.io/v1alpha1")
	obj.SetKind("Application")
	obj.SetName(name)
	obj.SetNamespace(argoCDNamespace)
	obj.SetLabels(labels)
	obj.Object["spec"] = spec
	return obj
}

// BuildWorkloadApplications builds one Argo CD Application per environment, each hydrating the
// placeholder workload from the dry branch into that environment's *-next branch, then syncing
// from the resulting active branch. Every Application carries instanceLabelKey so
// ArgoCDCommitStatus's applicationSelector can find them.
func BuildWorkloadApplications(inst Instance, repoURL string) []*unstructured.Unstructured {
	apps := make([]*unstructured.Unstructured, 0, len(environments))
	for _, env := range environments {
		name := fmt.Sprintf("%s-%s", inst.Name, env)
		spec := map[string]any{
			"project": "default",
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": name,
			},
			"sourceHydrator": map[string]any{
				"drySource": map[string]any{
					"repoURL":        repoURL,
					pathKey:          appsPath,
					"targetRevision": "HEAD",
				},
				"hydrateTo": map[string]any{
					"targetBranch": EnvironmentNextBranch(env),
				},
				"syncSource": map[string]any{
					"targetBranch": EnvironmentBranch(env),
					pathKey:        appsPath,
				},
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{
					"prune":      true,
					"allowEmpty": true,
					"selfHeal":   true,
				},
				"syncOptions": []any{"CreateNamespace=true"},
			},
		}
		apps = append(apps, newApplication(name, spec, map[string]string{instanceLabelKey: inst.Name}))
	}
	return apps
}

// BuildPromotionAppApplication builds the plain-sync Argo CD Application that deploys the
// gating CRDs from the repo's promotion-app/ directory (mirrors the reference repo's
// promotion-strategy.yaml Application - no sourceHydrator, just a direct sync from the dry
// branch).
func BuildPromotionAppApplication(inst Instance, repoURL string) *unstructured.Unstructured {
	spec := map[string]any{
		"project": "default",
		"destination": map[string]any{
			"server":    "https://kubernetes.default.svc",
			"namespace": inst.Namespace,
		},
		"source": map[string]any{
			"repoURL":        repoURL,
			pathKey:          PromotionAppDir,
			"targetRevision": "HEAD",
		},
		"syncPolicy": map[string]any{
			"automated": map[string]any{
				"prune":      true,
				"allowEmpty": true,
				"selfHeal":   true,
			},
		},
	}
	return newApplication(inst.Name+"-promotion-app", spec, map[string]string{instanceLabelKey: inst.Name})
}

// ApplyArgoCDApplications applies the promotion-app Application and one workload Application
// per environment via the controller-runtime client.
func ApplyArgoCDApplications(ctx context.Context, c client.Client, inst Instance, repoURL string) error {
	if err := applyObject(ctx, c, BuildPromotionAppApplication(inst, repoURL)); err != nil {
		return err
	}
	for _, app := range BuildWorkloadApplications(inst, repoURL) {
		if err := applyObject(ctx, c, app); err != nil {
			return err
		}
	}
	return nil
}

// PushPromotionApp marshals every gating CRD to YAML and pushes them, plus the placeholder
// workload manifest, into the repo's promotion-app/ (and apps/) directories on the dry branch -
// the ModeArgoCD equivalent of applying them directly, mirroring cmd/demo/promoter_crds.go's
// UploadManifests pattern but via a local clone instead of the GitHub contents API (we already
// have a local clone open for the git bootstrap step).
func PushPromotionApp(ctx context.Context, cfg *Config, owner string, inst Instance, resources []any) error {
	dir, cleanup, err := cloneRepo(ctx, cfg, owner, inst.RepoName)
	if err != nil {
		return err
	}
	defer cleanup()

	dry, err := defaultBranch(ctx, dir)
	if err != nil {
		return err
	}

	for i, res := range resources {
		out, err := yaml.Marshal(res)
		if err != nil {
			return fmt.Errorf("failed to marshal promotion-app resource %d: %w", i, err)
		}
		name := fmt.Sprintf("%s/resource-%02d.yaml", PromotionAppDir, i)
		if err := writeFile(dir, name, string(out)); err != nil {
			return err
		}
	}
	if err := writeFile(dir, "apps/workload.yaml", placeholderWorkloadManifest); err != nil {
		return err
	}

	if _, err := runGit(ctx, dir, "add", PromotionAppDir, "apps"); err != nil {
		return err
	}
	commitMsg := "chore: push loadtest promotion-app + placeholder workload"
	if _, err := runGit(ctx, dir, "commit", "--allow-empty", "-m", commitMsg); err != nil {
		return fmt.Errorf("failed to commit promotion-app: %w", err)
	}
	if _, err := runGit(ctx, dir, "push", "origin", dry); err != nil {
		return fmt.Errorf("failed to push promotion-app: %w", err)
	}
	return nil
}

// gatingResources returns every gating CR (as generic values, ready for YAML marshaling) for
// inst - the set pushed to promotion-app/ in ModeArgoCD, or applied directly in ModeDirect.
func gatingResources(cfg *Config, inst Instance) []any {
	wrcsList := BuildWebRequestCommitStatuses(cfg, inst)
	resources := make([]any, 0, 3+len(wrcsList))
	resources = append(resources,
		BuildPromotionStrategy(cfg, inst),
		BuildTimedCommitStatus(cfg, inst),
		BuildGitCommitStatus(inst),
	)
	for _, wrcs := range wrcsList {
		resources = append(resources, wrcs)
	}
	return resources
}
