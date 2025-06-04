import { create } from 'zustand';

// Store for current namespace and list of namespaces
export const namespaceStore = create<{
  namespace: string;
  namespaces: string[];
  setNamespace: (namespace: string) => void;
  setNamespaces: (namespaces: string[]) => void;
}>((set) => ({
  namespace: '',
  namespaces: [],
  setNamespace: (namespace) => set({ namespace }),
  setNamespaces: (namespaces) => set({ namespaces }),
})); 