import React from 'react';
import { FaServer } from 'react-icons/fa';
import { StatusIcon, statusLabel } from './StatusIcon';
import './EnvironmentCard.scss';
import CommitCard from './CommitCard';

const borderClass = (phase: string) => {
  switch (phase) {
    case 'success': return 'env-card--success';
    case 'pending': return 'env-card--pending';
    case 'failure': return 'env-card--failure';
    default: return 'env-card--default';
  }
};

//Props: 
//Environments: For active (dry/hydrated) branches and commit messages/urls
//SpecEnvs: For autoMerge
//gitRepositories: array of objects with metadata.name, spec.github.owner, spec.github.name, spec.repoURL
export interface EnvironmentCardProps {
  environments: any[];
  mergeSpecs: any[];
  gitRepositories: any[];
}

const EnvironmentCard: React.FC<EnvironmentCardProps> = ({ environments, mergeSpecs, gitRepositories }) => {
  return (
    <div className="env-cards-container">
      {environments.map((env: any, idx: number) => {
        const branch = env.branch?.replace(/^environments\//, '') || env.branch;
        const phase = env.active?.commitStatuses?.[0]?.phase || 'unknown';
        const autoMerge = mergeSpecs[idx]?.autoMerge !== undefined ? String(mergeSpecs[idx].autoMerge) : 'false';
        const lastSync = env.active?.hydrated?.commitTime ? new Date(env.active.hydrated.commitTime).toLocaleString() : '-';
        const dryCommitAuthor = env.dryCommitAuthor || '-';
        const dryCommitMessage = env.dryCommitMessage || '-';
        const totalChecks = (env.checks || []).length;
        const checksInProgress = (env.checks || []).filter((c: any) => c.status === 'pending' || c.status === 'progressing').length;



        // Sent to commitcard for border color
        let commitCardStatus = 'unknown';
        if (checksInProgress > 0) {
          commitCardStatus = 'pending';
        } else if (totalChecks > 0 && (env.checks || []).every((c: any) => c.status === 'success')) {
          commitCardStatus = 'success';
        } else if (totalChecks > 0 && (env.checks || []).some((c: any) => c.status === 'failure')) {
          commitCardStatus = 'failure';
        }

        return (
          <div key={env.branch} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', position: 'relative' }}>
            <div
              className={`env-card ${borderClass(phase)}`}
              style={{ position: 'relative' }}
            >
              <div className="env-card__header">
                <FaServer className="env-card__icon" />
                <span className="env-card__env-name">{branch}</span>
              </div>
              <div className="env-card__status-row">
                <StatusIcon phase={phase} type="health" />
                <span className={`env-card__status-label env-card__status-label--${phase}`}>{statusLabel(phase)}</span>
              </div>
              <div className="env-card__field-group">
                <div className="env-card__row"><span className="env-card__label">Commit SHA:</span> {env.active?.hydrated?.sha ? (env.hydratedCommitUrl ? (<a className="env-card__value env-card__commit-link" href={env.hydratedCommitUrl} target="_blank" rel="noopener noreferrer"><span className="env-card__value-field">{env.active.hydrated.sha.slice(0, 7)}</span></a>) : (<span className="env-card__value"><span className="env-card__value-field">{env.active.hydrated.sha.slice(0, 7)}</span></span>)) : (<span className="env-card__value"><span className="env-card__value-field">-</span></span>)}</div>
                <div className="env-card__row"><span className="env-card__label">Auto Merge:</span> <span className="env-card__value"><span className="env-card__value-field">{autoMerge}</span></span></div>
                <div className="env-card__row"><span className="env-card__label">Author:</span> <span className="env-card__value"><span className="env-card__value-field">{dryCommitAuthor}</span></span></div>
                <div className="env-card__row"><span className="env-card__label">Message:</span> <span className="env-card__value"><span className="env-card__value-field">{dryCommitMessage}</span></span></div>
                <div className="env-card__row"><span className="env-card__label">Last Sync:</span> <span className="env-card__value"><span className="env-card__value-field">{lastSync}</span></span></div>
              </div>
            </div>


            {/* Commit Card */}
            <CommitCard
              expanded={true}
              hideOnSuccess={false}
              commit={{
                sha: env.active?.hydrated?.sha ? env.active.hydrated.sha.slice(0, 7) : '',
                link: env.hydratedCommitUrl || '',
                author: env.hydratedCommitAuthor || '-',
                message: env.hydratedCommitMessage || '-',
                checks: env.checks || [],
                checksInProgress,
                totalChecks,
                status: commitCardStatus,
              }}
            />
          </div>
        );
      })}
    </div>
  );
};

export default EnvironmentCard;
