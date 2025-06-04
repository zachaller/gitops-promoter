import { createCRDStore } from './CRDStore';

// Minimal type definition for ArgoCDCommitStatus
export type ArgoCDCommitStatusType = {
  metadata: {
    name: string;
    namespace: string;
    [key: string]: any;
  };
  spec?: any;
  status?: any;
};

const baseStore = createCRDStore<ArgoCDCommitStatusType>('argocdcommitstatus', 'ArgoCDCommitStatus');

// items -> commitStatuses, fetchItems -> fetchCommitStatuses
export const ArgoCDCommitStatusStore = () => {
  const { items, fetchItems, ...rest } = baseStore();
  return {
    commitStatuses: items,
    fetchCommitStatuses: fetchItems,
    ...rest,
  };
}; 