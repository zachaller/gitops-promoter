package loadtest

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// baseURLPlaceholder is substituted with Config.ChangeMgmtBaseURL at build time. A token (not
// "{{ }}") is used so it can't collide with the Go template syntax the rest of these strings use.
const baseURLPlaceholder = "__CHANGE_MGMT_BASE_URL__"

const (
	headerAccept      = "Accept"
	headerContentType = "Content-Type"
	headerJSON        = "application/json"
)

// BuildWebRequestCommitStatuses builds the change-management-open/-approval/-close trio.
//
// This is a generalized copy of the change-management WebRequestCommitStatus trio drafted in
// webrequest-change-management-open-auto-success.yaml: the CEL trigger/fingerprint/gating
// expressions are unchanged, but the org-specific bits (argocd.intuit.com/* namespace-label
// lookups, devportal.intuit.com deep links, multi-tier commit-author lookup, and the verbose
// change-record markdown body) are replaced with Config.ChangeMgmtBaseURL and
// PromotionStrategy.Name.
func BuildWebRequestCommitStatuses(cfg *Config, inst Instance) []*promoterv1alpha1.WebRequestCommitStatus {
	psRef := promoterv1alpha1.ObjectReference{Name: inst.PromotionStrategyName()}
	return []*promoterv1alpha1.WebRequestCommitStatus{
		buildChangeManagementOpen(cfg, inst, psRef),
		buildChangeManagementApproval(cfg, inst, psRef),
		buildChangeManagementClose(cfg, inst, psRef),
	}
}

func withBaseURL(s string, cfg *Config) string {
	return strings.ReplaceAll(s, baseURLPlaceholder, cfg.ChangeMgmtBaseURL)
}

func webRequestObjectMeta(inst Instance, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: inst.Namespace}
}

// buildChangeManagementOpen opens a change record (POST) once the proposed dry SHA is aligned
// across all environments and a promotion PR exists on the gated (production) environment.
func buildChangeManagementOpen(
	cfg *Config, inst Instance, psRef promoterv1alpha1.ObjectReference,
) *promoterv1alpha1.WebRequestCommitStatus {
	const triggerVariables = `
let key = "` + ChangeManagementOpenKey + `";
let specEnvs = PromotionStrategy.Spec.Environments ?? [];
let statusEnvs = PromotionStrategy.Status.Environments ?? [];
let keyIsGlobal = any(PromotionStrategy.Spec.ProposedCommitStatuses ?? [], {#.Key == key});
let keyedEnvs = filter(specEnvs, { keyIsGlobal || any(#.ProposedCommitStatuses ?? [], {#.Key == key}) });
let firstGatedBranch = len(keyedEnvs) > 0 ? keyedEnvs[0].Branch : "";
let firstGatedIdx = firstGatedBranch != "" ? findIndex(specEnvs, { #.Branch == firstGatedBranch }) : nil;
let preGatedEnvs = (firstGatedIdx != nil && firstGatedIdx > 0) ? take(specEnvs, firstGatedIdx) : [];
let preGateNoOpenPR = all(preGatedEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	status == nil || status.PullRequest == nil || string(status.PullRequest.State) != "open"
});
let envShas = map(specEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	{
		branch: env.Branch,
		sha: status != nil && status.Proposed.Note != nil && status.Proposed.Note.DrySha != ""
			? status.Proposed.Note.DrySha : ""
	}
});
let everyEnvHasDrySha = len(envShas) > 0 && all(envShas, { #.sha != "" });
let canonicalNoteDrySha = len(envShas) > 0 ? envShas[0].sha : "";
let allNoteDryShasMatch = everyEnvHasDrySha && all(envShas, { #.sha == canonicalNoteDrySha });
let fingerprint = join(map(envShas, { #.branch + ":" + #.sha }), "|");
let gatedEnvsWithOpenPR = filter(keyedEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	status != nil && status.PullRequest != nil && string(status.PullRequest.State) == "open"
});
let hasOpenPRForKey = len(gatedEnvsWithOpenPR) > 0;
let lowestOpenGatedStatus = len(gatedEnvsWithOpenPR) > 0
	? find(statusEnvs, { #.Branch == gatedEnvsWithOpenPR[0].Branch })
	: nil;
let otherProposedChecksSuccess = lowestOpenGatedStatus != nil && all(
	filter(lowestOpenGatedStatus.Proposed.CommitStatuses ?? [], {
		#.Key != "` + ChangeManagementOpenKey + `" && #.Key != "` + ChangeManagementApprovalKey + `"
	}),
	{ string(#.Phase) == "success" }
);
let lastStatusCode = ResponseOutput != nil ? ResponseOutput.statusCode : 0;
let isRetryable = lastStatusCode == 429 || lastStatusCode >= 500;
let isNewFingerprint = fingerprint != (TriggerOutput.lastFingerprint ?? "");
let needsRetry = !isNewFingerprint && Phase != "success" && isRetryable;
{
	hasOpenPRForKey: hasOpenPRForKey,
	allNoteDryShasMatch: allNoteDryShasMatch,
	isNewFingerprint: isNewFingerprint,
	needsRetry: needsRetry,
	preGateNoOpenPR: preGateNoOpenPR,
	otherProposedChecksSuccess: otherProposedChecksSuccess,
	fingerprint: fingerprint,
	canonicalNoteDrySha: canonicalNoteDrySha
}
`
	const triggerExpr = `Variables.hasOpenPRForKey && Variables.allNoteDryShasMatch && ` +
		`(Variables.isNewFingerprint || Variables.needsRetry) && ` +
		`Variables.preGateNoOpenPR && Variables.otherProposedChecksSuccess`
	const triggerOutputExpr = `
let shouldTrigger = ` + triggerExpr + `;
{
	lastFingerprint: shouldTrigger ? Variables.fingerprint : (TriggerOutput.lastFingerprint ?? ""),
	shouldTrigger: shouldTrigger,
	fingerprint: Variables.fingerprint,
	canonicalNoteDrySha: Variables.canonicalNoteDrySha,
	evaluatedAt: string(now())
}
`
	const responseOutputExpr = `
{
	statusCode: Response.StatusCode,
	changeId: Response.Body.id == nil ? "" : string(Response.Body.id),
	lastRequestedAt: string(now())
}
`
	const successVariablesExpr = `
let specEnvs = PromotionStrategy.Spec.Environments ?? [];
let statusEnvs = PromotionStrategy.Status.Environments ?? [];
let fingerprint = join(map(specEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	env.Branch + ":" + (status != nil && status.Proposed.Note != nil && status.Proposed.Note.DrySha != ""
		? status.Proposed.Note.DrySha : "")
}), "|");
{ fingerprint: fingerprint }
`
	const successExpr = `
Response != nil
	? (Response.StatusCode == 202 && Response.Body != nil && Response.Body.id != nil && Response.Body.id != "")
	: (Phase == "success" && Variables.fingerprint == (TriggerOutput["lastFingerprint"] ?? ""))
`
	const bodyTemplate = `{
  "asset_id": "{{ .PromotionStrategy.Name }}",
  "on_behalf_of": "gitops-promoter-loadtest",
  "short_description": "Automated production promotion process",
  "commit_id": "{{ index .TriggerVariables "canonicalNoteDrySha" }}",
  "environment": "production",
  "start_time": "{{ now | date "2006-01-02T15:04:05Z07:00" }}",
  "end_time": "{{ now | dateModify "+8h" | date "2006-01-02T15:04:05Z07:00" }}"
}`

	return &promoterv1alpha1.WebRequestCommitStatus{
		TypeMeta:   promoterTypeMeta("WebRequestCommitStatus"),
		ObjectMeta: webRequestObjectMeta(inst, inst.WebRequestCommitStatusName("open")),
		Spec: promoterv1alpha1.WebRequestCommitStatusSpec{
			PromotionStrategyRef: psRef,
			Key:                  ChangeManagementOpenKey,
			ReportOn:             "proposed",
			DescriptionTemplate: `{{ .Phase }}: {{ if .ResponseOutput }}` +
				`{{ $cid := index .ResponseOutput "changeId" | default "" }}` +
				`{{ if ne $cid "" }}created change {{ $cid }}` +
				`{{ else }}change request accepted, will retry{{ end }}` +
				`{{ else }}preparing change record{{ end }}`,
			HTTPRequest: promoterv1alpha1.HTTPRequestSpec{
				MethodTemplate: "POST",
				Timeout:        metav1.Duration{Duration: 30 * time.Second},
				HeaderTemplates: map[string]string{
					headerAccept:      headerJSON,
					headerContentType: headerJSON,
				},
				URLTemplate:  withBaseURL(baseURLPlaceholder+"/change/open", cfg),
				BodyTemplate: bodyTemplate,
			},
			Success: promoterv1alpha1.SuccessSpec{
				When: promoterv1alpha1.WhenWithOutputSpec{
					Variables:  &promoterv1alpha1.OutputSpec{Expression: successVariablesExpr},
					Expression: successExpr,
				},
			},
			Mode: promoterv1alpha1.ModeSpec{
				Context: promoterv1alpha1.ContextPromotionStrategy,
				Trigger: &promoterv1alpha1.TriggerModeSpec{
					RequeueDuration: metav1.Duration{Duration: 30 * time.Second},
					When: promoterv1alpha1.WhenWithOutputSpec{
						Variables:  &promoterv1alpha1.OutputSpec{Expression: triggerVariables},
						Expression: triggerExpr,
						Output:     &promoterv1alpha1.OutputSpec{Expression: triggerOutputExpr},
					},
					Response: &promoterv1alpha1.ResponseOutputSpec{
						Output: promoterv1alpha1.OutputSpec{Expression: responseOutputExpr},
					},
				},
			},
		},
	}
}

// buildChangeManagementApproval polls for an approved (and currently in-window) change record
// matching the proposed dry SHA. Becomes success once one is found.
func buildChangeManagementApproval(
	cfg *Config, inst Instance, psRef promoterv1alpha1.ObjectReference,
) *promoterv1alpha1.WebRequestCommitStatus {
	const triggerVariables = `
let key = "` + ChangeManagementApprovalKey + `";
let specEnvs = PromotionStrategy.Spec.Environments ?? [];
let statusEnvs = PromotionStrategy.Status.Environments ?? [];
let keyIsGlobal = any(PromotionStrategy.Spec.ProposedCommitStatuses ?? [], {#.Key == key});
let firstGatedIdx = findIndex(specEnvs, { keyIsGlobal || any(#.ProposedCommitStatuses ?? [], {#.Key == key}) });
let preGatedEnvs = (firstGatedIdx != nil && firstGatedIdx > 0) ? take(specEnvs, firstGatedIdx) : [];
let preGateNoOpenPR = all(preGatedEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	status == nil || status.PullRequest == nil || string(status.PullRequest.State) != "open"
});
let envShas = map(specEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	{
		branch: env.Branch,
		sha: status != nil && status.Proposed.Note != nil && status.Proposed.Note.DrySha != ""
			? status.Proposed.Note.DrySha : ""
	}
});
let everyEnvHasDrySha = len(envShas) > 0 && all(envShas, { #.sha != "" });
let canonicalNoteDrySha = len(envShas) > 0 ? envShas[0].sha : "";
let allNoteDryShasMatch = everyEnvHasDrySha && all(envShas, { #.sha == canonicalNoteDrySha });
let fingerprint = join(map(envShas, { #.branch + ":" + #.sha }), "|");
let hasOpenPRForKey = any(specEnvs, {
	let env = #;
	let envHasKey = keyIsGlobal || any(env.ProposedCommitStatuses ?? [], {#.Key == key});
	let status = find(statusEnvs, { #.Branch == env.Branch });
	envHasKey && status != nil && status.PullRequest != nil && string(status.PullRequest.State) == "open"
});
let isFirstRun = TriggerOutput.lastRequestTime == nil;
let isNewFingerprint = fingerprint != (TriggerOutput.lastFingerprint ?? "");
let pollingIntervalElapsed = !isFirstRun && ((now() - date(TriggerOutput.lastRequestTime)) >= duration("30s"));
{
	hasOpenPRForKey: hasOpenPRForKey,
	allNoteDryShasMatch: allNoteDryShasMatch,
	preGateNoOpenPR: preGateNoOpenPR,
	fingerprint: fingerprint,
	canonicalNoteDrySha: canonicalNoteDrySha,
	isFirstRun: isFirstRun,
	isNewFingerprint: isNewFingerprint,
	pollingIntervalElapsed: pollingIntervalElapsed
}
`
	const triggerExpr = `Variables.hasOpenPRForKey && Variables.allNoteDryShasMatch && ` +
		`(Variables.isFirstRun || Variables.isNewFingerprint || Variables.pollingIntervalElapsed) && ` +
		`Variables.preGateNoOpenPR`
	const triggerOutputExpr = `
let shouldTrigger = ` + triggerExpr + `;
{
	lastFingerprint: shouldTrigger ? Variables.fingerprint : (TriggerOutput.lastFingerprint ?? ""),
	lastRequestTime: shouldTrigger ? now() : TriggerOutput.lastRequestTime,
	fingerprint: Variables.fingerprint,
	canonicalNoteDrySha: Variables.canonicalNoteDrySha,
	evaluatedAt: string(now())
}
`
	const responseOutputExpr = `
let approvedRecords = filter(Response.Body.change_records ?? [], {
	date(#.change_request.start_time) <= now() && date(#.change_request.end_time) >= now() &&
	((#.status ?? "") == "APPROVED" || (#.status ?? "") == "CONDITIONALLY_APPROVED")
});
{
	statusCode: Response.StatusCode,
	totalRecordCount: len(Response.Body.change_records ?? []),
	approvedCount: len(approvedRecords),
	lastCheckedAt: string(now())
}
`
	const successExpr = `
Response != nil
	? (Response.StatusCode == 200 &&
	   len(Response.Body.change_records) > 0 &&
	   any(Response.Body.change_records, {
	       date(#.change_request.start_time) <= now() && date(#.change_request.end_time) >= now() &&
	       ((#.status ?? "") == "APPROVED" || (#.status ?? "") == "CONDITIONALLY_APPROVED")
	   }))
	: (Phase == "success" && Variables.fingerprint == (TriggerOutput["lastFingerprint"] ?? ""))
`
	const successVariablesExpr = `
let specEnvs = PromotionStrategy.Spec.Environments ?? [];
let statusEnvs = PromotionStrategy.Status.Environments ?? [];
let fingerprint = join(map(specEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	env.Branch + ":" + (status != nil && status.Proposed.Note != nil && status.Proposed.Note.DrySha != ""
		? status.Proposed.Note.DrySha : "")
}), "|");
{ fingerprint: fingerprint }
`

	return &promoterv1alpha1.WebRequestCommitStatus{
		TypeMeta:   promoterTypeMeta("WebRequestCommitStatus"),
		ObjectMeta: webRequestObjectMeta(inst, inst.WebRequestCommitStatusName("approval")),
		Spec: promoterv1alpha1.WebRequestCommitStatusSpec{
			PromotionStrategyRef: psRef,
			Key:                  ChangeManagementApprovalKey,
			ReportOn:             "proposed",
			DescriptionTemplate: `{{ .Phase }}: {{ if .ResponseOutput }}` +
				`{{ index .ResponseOutput "approvedCount" | default 0 }}` +
				`/{{ index .ResponseOutput "totalRecordCount" | default 0 }} records approved` +
				`{{ else }}checking for approval{{ end }}`,
			HTTPRequest: promoterv1alpha1.HTTPRequestSpec{
				MethodTemplate: "GET",
				Timeout:        metav1.Duration{Duration: 30 * time.Second},
				HeaderTemplates: map[string]string{
					headerAccept: headerJSON,
				},
				URLTemplate: withBaseURL(
					baseURLPlaceholder+
						`/changes/search?asset_id={{ .PromotionStrategy.Name }}`+
						`&commit_id={{ index .TriggerVariables "canonicalNoteDrySha" }}&limit=25`,
					cfg,
				),
			},
			Success: promoterv1alpha1.SuccessSpec{
				When: promoterv1alpha1.WhenWithOutputSpec{
					Variables:  &promoterv1alpha1.OutputSpec{Expression: successVariablesExpr},
					Expression: successExpr,
				},
			},
			Mode: promoterv1alpha1.ModeSpec{
				Context: promoterv1alpha1.ContextPromotionStrategy,
				Trigger: &promoterv1alpha1.TriggerModeSpec{
					RequeueDuration: metav1.Duration{Duration: 15 * time.Second},
					When: promoterv1alpha1.WhenWithOutputSpec{
						Variables:  &promoterv1alpha1.OutputSpec{Expression: triggerVariables},
						Expression: triggerExpr,
						Output:     &promoterv1alpha1.OutputSpec{Expression: triggerOutputExpr},
					},
					Response: &promoterv1alpha1.ResponseOutputSpec{
						Output: promoterv1alpha1.OutputSpec{Expression: responseOutputExpr},
					},
				},
			},
		},
	}
}

// buildChangeManagementClose closes the change record (search then POST) after promotion PRs
// on the gated (production) environment have merged. Never reports failure.
func buildChangeManagementClose(
	cfg *Config, inst Instance, psRef promoterv1alpha1.ObjectReference,
) *promoterv1alpha1.WebRequestCommitStatus {
	const triggerVariables = `
let key = "` + ChangeManagementCloseKey + `";
let specEnvs = PromotionStrategy.Spec.Environments ?? [];
let statusEnvs = PromotionStrategy.Status.Environments ?? [];
let keyIsGlobal = any(PromotionStrategy.Spec.ActiveCommitStatuses ?? [], {#.Key == key});
let gatedEnvs = filter(specEnvs, { keyIsGlobal || any(#.ActiveCommitStatuses ?? [], {#.Key == key}) });
let allGatedPRsMerged = len(gatedEnvs) > 0 && all(gatedEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	status == nil || status.PullRequest == nil || string(status.PullRequest.State) != "open"
});
let envShas = map(specEnvs, {
	let env = #;
	let status = find(statusEnvs, { #.Branch == env.Branch });
	{
		branch: env.Branch,
		sha: status != nil && status.Proposed.Note != nil && status.Proposed.Note.DrySha != ""
			? status.Proposed.Note.DrySha : ""
	}
});
let everyEnvHasDrySha = len(envShas) > 0 && all(envShas, { #.sha != "" });
let canonicalNoteDrySha = len(envShas) > 0 ? envShas[0].sha : "";
let allNoteDryShasMatch = everyEnvHasDrySha && all(envShas, { #.sha == canonicalNoteDrySha });
let fingerprint = join(map(envShas, { #.branch + ":" + #.sha }), "|");
let priorStep = ResponseOutput != nil ? (ResponseOutput.step ?? "") : "";
let priorChangeId = ResponseOutput != nil ? (ResponseOutput.changeId ?? "") : "";
let haveChangeIdToClose = priorChangeId != "" && (priorStep == "found" || priorStep == "closeRetry");
let alreadyDoneForFingerprint = fingerprint != "" && fingerprint == (TriggerOutput.lastDoneFingerprint ?? "");
let isNewFingerprint = fingerprint != (TriggerOutput.lastFingerprint ?? "");
let needsRetry = !isNewFingerprint && priorStep == "closeRetry";
{
	allGatedPRsMerged: allGatedPRsMerged,
	allNoteDryShasMatch: allNoteDryShasMatch,
	fingerprint: fingerprint,
	canonicalNoteDrySha: canonicalNoteDrySha,
	haveChangeIdToClose: haveChangeIdToClose,
	alreadyDoneForFingerprint: alreadyDoneForFingerprint,
	isNewFingerprint: isNewFingerprint,
	needsRetry: needsRetry,
	priorStep: priorStep,
	priorChangeId: priorChangeId,
	inCloseMode: haveChangeIdToClose
}
`
	const triggerExpr = `Variables.allNoteDryShasMatch && !Variables.alreadyDoneForFingerprint && ` +
		`(Variables.isNewFingerprint || Variables.haveChangeIdToClose || Variables.needsRetry) && ` +
		`Variables.allGatedPRsMerged`
	const triggerOutputExpr = `
let shouldTrigger = ` + triggerExpr + `;
let priorMatchesCurrent = Variables.fingerprint != "" && Variables.fingerprint == (TriggerOutput.lastFingerprint ?? "");
let justDone = priorMatchesCurrent && (Variables.priorStep == "closed" || Variables.priorStep == "closeFailedTerminal");
let isFiringClosePost = shouldTrigger && Variables.inCloseMode && Variables.haveChangeIdToClose;
{
	lastFingerprint: shouldTrigger ? Variables.fingerprint : (TriggerOutput.lastFingerprint ?? ""),
	lastDoneFingerprint: justDone ? Variables.fingerprint : (TriggerOutput.lastDoneFingerprint ?? ""),
	lastClosedChangeId: isFiringClosePost ? Variables.priorChangeId : (TriggerOutput.lastClosedChangeId ?? ""),
	shouldTrigger: shouldTrigger,
	fingerprint: Variables.fingerprint,
	canonicalNoteDrySha: Variables.canonicalNoteDrySha,
	phase: Phase,
	evaluatedAt: string(now())
}
`
	const responseOutputExpr = `
let isSearch = Response.Body != nil && (Response.Body.change_records ?? nil) != nil;
let priorChangeId = ResponseOutput != nil ? (ResponseOutput.changeId ?? "") : "";
let inWindowRecords = isSearch ? filter(Response.Body.change_records ?? [], {
	date(#.change_request.start_time) <= now() && date(#.change_request.end_time) >= now()
}) : [];
let foundChangeId = isSearch
	? (len(inWindowRecords) > 0 ? string(inWindowRecords[0].id ?? "")
	    : (len(Response.Body.change_records ?? []) > 0 ? string((Response.Body.change_records ?? [])[0].id ?? "") : ""))
	: priorChangeId;
let statusCode = Response.StatusCode;
let closeStep = statusCode >= 200 && statusCode < 300
	? "closed"
	: (statusCode == 429 || statusCode >= 500 ? "closeRetry" : "closeFailedTerminal");
let step = isSearch ? (foundChangeId != "" ? "found" : "searching") : closeStep;
{
	statusCode: statusCode,
	step: step,
	changeId: foundChangeId,
	lastRequestedAt: string(now())
}
`
	const successExpr = `
Response != nil
	? (Response.Body != nil && (Response.Body.change_records ?? nil) != nil
	    ? len(Response.Body.change_records ?? []) == 0
	    : ((Response.StatusCode >= 200 && Response.StatusCode < 300) ||
	       (Response.StatusCode >= 400 && Response.StatusCode != 429 && Response.StatusCode < 500)))
	: (ResponseOutput == nil || ((ResponseOutput.step ?? "") != "found" && (ResponseOutput.step ?? "") != "closeRetry"))
`

	return &promoterv1alpha1.WebRequestCommitStatus{
		TypeMeta:   promoterTypeMeta("WebRequestCommitStatus"),
		ObjectMeta: webRequestObjectMeta(inst, inst.WebRequestCommitStatusName("close")),
		Spec: promoterv1alpha1.WebRequestCommitStatusSpec{
			PromotionStrategyRef: psRef,
			Key:                  ChangeManagementCloseKey,
			ReportOn:             "active",
			DescriptionTemplate: `{{ .Phase }}: {{ if .ResponseOutput }}` +
				`{{ index .ResponseOutput "step" | default "searching" }}` +
				`{{ else }}searching for change record to close{{ end }}`,
			HTTPRequest: promoterv1alpha1.HTTPRequestSpec{
				MethodTemplate: `{{ if index .TriggerVariables "inCloseMode" }}POST{{ else }}GET{{ end }}`,
				Timeout:        metav1.Duration{Duration: 30 * time.Second},
				HeaderTemplates: map[string]string{
					headerAccept:      headerJSON,
					headerContentType: headerJSON,
				},
				URLTemplate: withBaseURL(
					`{{ if index .TriggerVariables "inCloseMode" }}`+
						baseURLPlaceholder+`/change/close/{{ index .TriggerVariables "priorChangeId" }}`+
						`{{ else }}`+baseURLPlaceholder+`/changes/search?asset_id={{ .PromotionStrategy.Name }}`+
						`&commit_id={{ index .TriggerVariables "canonicalNoteDrySha" }}&limit=25{{ end }}`,
					cfg,
				),
				BodyTemplate: `{{ if index .TriggerVariables "inCloseMode" }}{"change_execution_status": "SUCCEEDED"}{{ end }}`,
			},
			Success: promoterv1alpha1.SuccessSpec{
				When: promoterv1alpha1.WhenWithOutputSpec{Expression: successExpr},
			},
			Mode: promoterv1alpha1.ModeSpec{
				Context: promoterv1alpha1.ContextPromotionStrategy,
				Trigger: &promoterv1alpha1.TriggerModeSpec{
					RequeueDuration: metav1.Duration{Duration: 30 * time.Second},
					When: promoterv1alpha1.WhenWithOutputSpec{
						Variables:  &promoterv1alpha1.OutputSpec{Expression: triggerVariables},
						Expression: triggerExpr,
						Output:     &promoterv1alpha1.OutputSpec{Expression: triggerOutputExpr},
					},
					Response: &promoterv1alpha1.ResponseOutputSpec{
						Output: promoterv1alpha1.OutputSpec{Expression: responseOutputExpr},
					},
				},
			},
		},
	}
}
