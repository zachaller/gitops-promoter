import { createCRDStore } from './CRDStore';
import type { ChangeTransferPolicyType } from '../models/ChangeTransferPolicyType';

export const ChangeTransferPolicyStore = createCRDStore<ChangeTransferPolicyType>('changetransferpolicy', 'ChangeTransferPolicy'); 