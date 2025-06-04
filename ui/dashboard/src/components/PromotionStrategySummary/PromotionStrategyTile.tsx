import { FaGitAlt } from 'react-icons/fa';
import { StatusIcon } from '@lib/components/StatusIcon';
import type { StatusType } from '@lib/components/StatusIcon'
import './PromotionStrategiesTiles.scss';

export const PromotionStrategyTile = ({ps, borderStatus, promotedPhase, lastUpdated,
  onClick
  }:{
    ps: any, namespace: string,
    borderStatus: 'success' | 'failure' | 'pending' | 'default',
    promotedPhase: 'success' | 'failure' | 'pending',
    lastUpdated: string,
    onClick: () => void
  }) => (

  <div
    className={`ps-tile ps-tile--${borderStatus}`}
    style={{ cursor: 'pointer' }}
    onClick={onClick}
  >


    <div className="ps-tile__header">
      <FaGitAlt className="ps-tile__icon" />
      <span className="ps-tile__name">{ps.metadata.name}</span>
    </div>
    <div className="ps-tile__row"><span className="ps-tile__label">Repository:</span> <span className="ps-tile__info">{ps.spec.gitRepositoryRef.name}</span></div>
    <div className="ps-tile__row"><span className="ps-tile__label">Promoted:</span> <span className="ps-tile__info"><StatusIcon phase={promotedPhase} type="status" /></span></div>
    <div className="ps-tile__row"><span className="ps-tile__label">Last Updated:</span> <span className="ps-tile__info">{lastUpdated}</span></div>
    <div className="ps-tile__row"><span className="ps-tile__label">Environments:</span></div>
    <div className="ps-tile__envs">


      {/* Specs: /PromotionStrategy/Status/Environments/Branch/Active/CommitStatus */}

      {ps.spec.environments.map((env: any) => {
        const envStatus = ps.status?.environments?.find((e: any) => e.branch === env.branch)?.active?.commitStatus?.phase || 'unknown';
        return (


          <div key={env.branch} className="ps-tile__env-row-grid">
            <span className="ps-tile__env-branch">
              {env.branch}{env.autoMerge && <span className="ps-tile__env-automerge"> (auto-merged)</span>}
            </span>
            <span className="ps-tile__env-status">
              <StatusIcon phase={envStatus as StatusType} type="status" />
            </span>
          </div>
        );
      })}
    </div>
  </div>
); 