export interface CommitStatus {
  key: string;
  phase: string;
  url?: string;
  details?: string;
}

export interface Commit {
  sha?: string;
  author?: string;
  subject?: string;
  body?: string;
  commitTime?: string | null;
  repoURL?: string;
  references?: Array<{
    commit: ReferenceCommit;
  }>;
}

export interface ReferenceCommit  {
  sha?: string;
  author?: string;
  subject?: string;
  body?: string;
  date?: string;
  url?: string;
  repoURL?: string;
}

export interface PullRequest {
  id: string;
  url?: string;
}

export interface History {
  active: {
    dry?: Commit;
    hydrated?: Commit;
    commitStatuses?: CommitStatus[];
  };
  proposed: {
    hydrated?: Commit;
    commitStatuses?: CommitStatus[];
  };
  pullRequest?: PullRequest;
}

export interface Environment {
  branch: string;
  active: {
    dry?: Commit;
    hydrated?: Commit;
    commitStatuses?: CommitStatus[];
  };
  proposed: {
    dry?: Commit;
    hydrated?: Commit;
    commitStatuses?: CommitStatus[];
  };
  pullRequest?: PullRequest;
  history?: History[];
}

export interface PromotionStrategy {
  kind: string;
  apiVersion: string;
  metadata: {
    name: string;
    namespace: string;
    uid: string;
    resourceVersion: string;
    generation: number;
    creationTimestamp: string;
    labels?: Record<string, string>;
    annotations?: Record<string, string>;
  };
  spec: {
    gitRepositoryRef: {
      name: string;
      namespace?: string;
    };
    activeCommitStatuses?: { key: string }[] | null;
    proposedCommitStatuses?: { key: string }[] | null;
    environments: {
      branch: string;
      autoMerge?: boolean;
      activeCommitStatuses?: { key: string }[] | null;
      proposedCommitStatuses?: { key: string }[] | null;
    }[];
  };
  status?: { environments?: Environment[] };
}

export interface Check {
  name: string;
  status: string;
  details?: string;
  url?: string;
}

export interface EnrichedEnvDetails {
  // Environment info
  branch: string;
  promotionStatus: string;
  
  // Active commits
  activeSha: string;
  activeCommitAuthor: string;
  activeCommitSubject: string;
  activeCommitMessage: string;
  activeCommitDate: string;
  activeCommitUrl: string;
  activeChecks: Check[];
  activeChecksSummary: { successCount: number; totalCount: number; shouldDisplay: boolean };
  activeStatus: 'success' | 'failure' | 'pending' | 'unknown';
  activePrUrl: string | null;
  activePrNumber: number | null;
  
  activeReferenceCommit: ReferenceCommit | null;
  activeReferenceCommitUrl: string | null;
  
  // Proposed commits
  proposedSha: string;
  prNumber: number | null;
  prUrl: string | null;
  proposedDryCommitAuthor: string;
  proposedDryCommitSubject: string;
  proposedDryCommitBody: string;
  proposedDryCommitDate: string;
  proposedDryCommitUrl: string;
  proposedChecks: Check[];
  proposedChecksSummary: { successCount: number; totalCount: number; shouldDisplay: boolean };
  proposedStatus: 'success' | 'failure' | 'pending' | 'unknown';

  proposedReferenceCommit: ReferenceCommit | null;
  proposedReferenceCommitUrl: string | null;
}

export type PromotionPhase = 'promoted' | 'failure' | 'pending' | 'unknown';

// ===== Aggregated Types (from PromotionStrategyView) =====

export interface GitRepositoryRef {
  name: string;
  namespace: string;
  spec?: {
    owner: string;
    name: string;
    scmProviderRef: {
      name: string;
      namespace?: string;
    };
  };
  status?: {
    observedGeneration?: number;
  };
}

export interface ChangeTransferPolicyRef {
  name: string;
  namespace: string;
  branch: string;
  spec?: {
    repositoryReference: {
      name: string;
    };
    proposedBranch: string;
    activeBranch: string;
    autoMerge?: boolean;
  };
  status?: {
    active?: {
      dry?: Commit;
      hydrated?: Commit;
      commitStatuses?: CommitStatus[];
    };
    proposed?: {
      dry?: Commit;
      hydrated?: Commit;
      commitStatuses?: CommitStatus[];
    };
  };
}

export interface OwnerReference {
  apiVersion: string;
  kind: string;
  name: string;
  uid: string;
  controller?: boolean;
  blockOwnerDeletion?: boolean;
}

// ResourceMetadata contains common metadata fields for aggregated resources.
// This mirrors the structure of Kubernetes ObjectMeta but only includes fields
// needed for identification and ownership tracking.
export interface ResourceMetadata {
  name: string;
  namespace: string;
  uid?: string;
  ownerReferences?: OwnerReference[];
}

export interface ArgoCDCommitStatusRef {
  metadata: ResourceMetadata;
  spec?: {
    applicationRef: {
      name: string;
      namespace?: string;
    };
    promotionStrategyRef: {
      name: string;
    };
  };
  status?: {
    observedGeneration?: number;
  };
}

export interface GitCommitStatusRef {
  metadata: ResourceMetadata;
  spec?: {
    promotionStrategyRef: {
      name: string;
    };
    sha?: string;
    name?: string;
    description?: string;
    phase?: string;
    url?: string;
  };
  status?: {
    observedGeneration?: number;
  };
}

export interface TimedCommitStatusRef {
  metadata: ResourceMetadata;
  spec?: {
    promotionStrategyRef: {
      name: string;
    };
    duration?: string;
    name?: string;
    description?: string;
  };
  status?: {
    observedGeneration?: number;
  };
}

export interface CommitStatusRef {
  // Metadata contains identifying information for the resource.
  // OwnerReferences can be used to find the parent commit status manager
  // (e.g., TimedCommitStatus, GitCommitStatus, ArgoCDCommitStatus).
  metadata: ResourceMetadata;
  spec?: {
    repositoryReference: {
      name: string;
    };
    sha: string;
    name: string;
    description?: string;
    phase?: string;
    url?: string;
  };
  status?: {
    id?: string;
    sha?: string;
    phase?: string;
    observedGeneration?: number;
  };
}

export interface PullRequestRef {
  name: string;
  namespace: string;
  branch: string;
  spec?: {
    repositoryReference: {
      name: string;
    };
    title?: string;
    description?: string;
    sourceBranch: string;
    targetBranch: string;
    state?: string;
  };
  status?: {
    id?: string;
    state?: string;
    specHash?: string;
    prCreationTime?: string;
  };
}

export interface CommitStatusAggregation {
  argoCD?: ArgoCDCommitStatusRef[];
  git?: GitCommitStatusRef[];
  timed?: TimedCommitStatusRef[];
  commitStatuses?: CommitStatusRef[];
}

export interface AggregatedResources {
  gitRepository?: GitRepositoryRef;
  changeTransferPolicies?: ChangeTransferPolicyRef[];
  commitStatuses?: CommitStatusAggregation;
  pullRequests?: PullRequestRef[];
}

// PromotionStrategyView is the aggregated view returned by the API
export interface PromotionStrategyView extends PromotionStrategy {
  aggregated?: AggregatedResources;
} 