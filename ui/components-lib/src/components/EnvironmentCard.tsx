import { FaServer, FaExternalLinkAlt } from 'react-icons/fa';
import { StatusIcon, statusLabel } from './StatusIcon';
import './EnvironmentCard.scss';
import PrCard from './PRCard';

//Props: 
//Environments: For active (dry/hydrated) branches and commit messages/urls
//gitRepositories: array of objects with metadata.name, spec.github.owner, spec.github.name, spec.repoURL
export interface EnvironmentCardProps {
  environments: any[];
}



const EnvironmentCard: React.FC<EnvironmentCardProps> = ({ environments }) => {

  // Find if any environment is pending/promoted using promotionStatus
  const activeEnvBranch = environments.find(
    (env: any) => env.promotionStatus === 'pending' || env.promotionStatus === 'promoted'
  )?.branch;
  const hasActivePR = !!activeEnvBranch;

  return (
    <div className="env-cards-container">
      {environments.map((env: any) => {
        const branch = env.branch;
        const phase = env.phase;
        const lastSync = env.lastSync;
        const dryCommitAuthor = env.dryCommitAuthor;
        const dryCommitMessage = env.dryCommitMessage;
        const activeChecks = env.activeChecks;
        const drySha = env.drySha;
        const dryCommitUrl = env.dryCommitUrl;

        // Construct ArgoCD URL (placeholder, update as needed)
        const argoCdUrl = env.argoCdUrl || 'https://argocd.example.com/applications?env=' + encodeURIComponent(env.branch || '');

        // Only render PR card if not 'success'
        let prCard = null;
        if (env.promotionStatus !== 'success') {
          let autoExpand = false;
          if (env.promotionStatus === 'promoted') {
            autoExpand = true;
          } else if (env.promotionStatus === 'pending') {
            autoExpand = false;
          }


          prCard = (
            <PrCard
              autoExpand={autoExpand}
              commit={{
                branch: env.branch,
                hydratedSha: env.hydratedSha,
                link: env.hydratedCommitUrl,
                author: env.hydratedCommitAuthor,
                message: env.hydratedCommitMessage,
                checks: env.proposedChecks,
                proposedSha: env.proposedSha,
                prCreatedAt: env.prCreatedAt,
                prNumber: env.prNumber,
                mergeDate: env.mergeDate,
                hydratedCommitUrl: env.hydratedCommitUrl,
                proposedBranch: env.proposedBranch,
                promotionStatus: env.promotionStatus,
                percent: env.percent,
                prUrl: env.prUrl,
              }}
            />
          );
        }

        return (
          <div key={env.branch}>
            <div className="env-card">
              <div className="env-card__header">
                <FaServer className="env-card__icon" />
                <span className="env-card__env-name">{branch}</span>
              </div>
              
              <div className="env-card__status-row">
                <StatusIcon phase={phase} type="health" />
                <span className={`env-card__status-label env-card__status-label--${phase}`}>{statusLabel(phase)}</span>
              </div>



              {/* ENVIRONMENT DETAILS */}
              <div className="env-card__field-group">
                <div className="env-card__row">
                  <span className="env-card__label">Active Commit:</span>
                  <span className="env-card__value">
                    {drySha !== '-' && dryCommitUrl !== '-' ? (
                      <a className="env-card__commit-link" href={dryCommitUrl} target="_blank" rel="noopener noreferrer">
                        <span className="env-card__value-field">{drySha}</span>
                      </a>
                    ) : (
                      <span className="env-card__value-field">{drySha}</span>
                    )}
                  </span>



                </div>
                <div className="env-card__row">
                  <span className="env-card__label">Author:</span>
                  <span className="env-card__value">
                    <span className="env-card__value-field">{dryCommitAuthor}</span>
                  </span>
                </div>



                <div className="env-card__row">
                  <span className="env-card__label">Message:</span>
                  <span className="env-card__value">
                    <span className="env-card__value-field">{dryCommitMessage}</span>
                  </span>
                </div>



                <div className="env-card__row">
                  <span className="env-card__label">Last Sync:</span>
                  <span className="env-card__value">
                    <span className="env-card__value-field">{lastSync}</span>
                  </span>
                </div>



                <div className="env-card__row">
                  <span className="env-card__label">Resource:</span>
                  <span className="env-card__value">
                    <a
                      href={argoCdUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      style={{ color: '#3069f0', display: 'inline-flex', alignItems: 'center' }}
                    >
                      <FaExternalLinkAlt style={{ fontSize: '14px' }} />
                    </a>
                  </span>
                </div>


                {/* ACTIVE CHECKS */}
                {activeChecks.length > 0 && (
                  <div className="pr-card__checks-list">
                    <div className="pr-card__label">Active Checks:</div>
                    <ul>


                      {activeChecks.map((check: any, idx: number) => (
                        <li key={check.name + '-' + idx} 
                            className="pr-card__check-item">
                          
                          <span className="pr-card__check-icon">
                            <StatusIcon phase={check.status} type="status" />
                          </span>


                          
                          <span className="pr-card__check-name">{check.name}</span>
                          {check.details && (
                            <a href={check.details} className="pr-card__check-link" target="_blank" rel="noopener noreferrer">View details</a>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            </div>
            {prCard}
          </div>
        );
      })}
    </div>
  );
};

export default EnvironmentCard;