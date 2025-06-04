import type { PromotionStrategyType } from '@shared/models/PromotionStrategyType';



export function getLastCommitTime(ps: PromotionStrategyType): Date | null {

    //Determine the last commit time between both active/proposed hydrated commit
  const commitTimes = [
    ...(ps.status?.environments?.map(env => env.active?.hydrated?.commitTime) || []),
    ...(ps.status?.environments?.map(env => env.proposed?.hydrated?.commitTime) || [])
  ].filter(Boolean);

  if (commitTimes.length) {
    return new Date(Math.max(...commitTimes.map(t => new Date(t as string).getTime())));
  }

  if (ps.metadata?.creationTimestamp) {
    return new Date(ps.metadata.creationTimestamp);
  }

  return null
}


//
export function getPromotionPhase(ps: PromotionStrategyType): { 
  borderStatus: 'success' | 'failure' | 'pending' | 'default', 
  promotedPhase: 'success' | 'failure' | 'pending' } {

    
    //Looks at all stasuses -> if all success/failure -> returns. Else, returns pending.
  const envPhases = ps.status?.environments?.map(env => env.active?.commitStatus?.phase) || [];
  if (envPhases.length > 0) {
    if (envPhases.every(phase => phase === 'success')) return { borderStatus: 'success', promotedPhase: 'success' };
    if (envPhases.some(phase => phase === 'failure')) return { borderStatus: 'failure', promotedPhase: 'failure' };
    if (envPhases.some(phase => phase === 'pending')) return { borderStatus: 'pending', promotedPhase: 'pending' };
    return { borderStatus: 'default', promotedPhase: 'pending' };
  }
  return { borderStatus: 'default', promotedPhase: 'success' };
} 