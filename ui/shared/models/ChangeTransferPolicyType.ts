export interface ChangeTransferPolicyType {
  kind: string;
  apiVersion: string;
  metadata: {
    name: string;
    namespace?: string;
    labels?: Record<string, string>;
    annotations?: Record<string, string>;
  };
  spec: {
    gitRepositoryRef: {
      name: string;
      namespace?: string;
    };
    proposedBranch: string;
    activeBranch: string;
    autoMerge?: boolean;
    activeCommitStatuses?: Array<{ key: string }>;
    proposedCommitStatuses?: Array<{ key: string }> | null;
  };
  status?: {
    proposed?: {
      dry?: { sha?: string };
      hydrated?: { sha?: string };
      commitStatuses?: Array<{
        key: string;
        phase?: 'pending' | 'success' | 'failure';
      }>;
    };
    active?: {
      dry?: { sha?: string };
      hydrated?: { sha?: string };
      commitStatuses?: Array<{
        key: string;
        phase?: 'pending' | 'success' | 'failure';
      }>;
    };
  };
} 