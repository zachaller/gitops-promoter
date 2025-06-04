import { createCRDStore } from './CRDStore';

export type GitRepositoryType = {
  metadata: {
    name: string;
    namespace: string;
    [key: string]: any;
  };
  spec?: any;
  status?: any;
};

export const GitRepositoryStore = createCRDStore<GitRepositoryType>('gitrepository', 'GitRepository'); 