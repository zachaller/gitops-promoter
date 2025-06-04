import { createCRDStore } from './CRDStore';

export type CommitStatusType = {
  metadata: {
    name: string;
    namespace: string;
    [key: string]: any;
  };
  spec?: any;
  status?: any;
};

const baseStore = createCRDStore<CommitStatusType>('commitstatus', 'CommitStatus');

export const CommitStatusStore = () => {
  const { items, fetchItems, subscribe, unsubscribe, ...rest } = baseStore();
  return {
    commitStatuses: items,
    fetchCommitStatuses: fetchItems,
    subscribe,
    unsubscribe,
    ...rest,
  };
}; 