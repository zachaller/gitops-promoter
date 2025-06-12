import React, { useEffect, useState, useRef } from 'react';
import './PRCard.scss';
import CircularProgress from './CircularProgress';
import { StatusIcon } from './StatusIcon';
import { GoGitPullRequest } from 'react-icons/go';
import { FiChevronDown, FiChevronUp } from 'react-icons/fi';
import { timeAgo } from '../../../shared/utils/util';

interface ProposedCheck {
  name: string;
  status: string;
  details: string;
}

interface PRDetails {
  branch: string;
  hydratedSha: string;
  link: string;
  author: string;
  message: string;
  checks: ProposedCheck[];
  proposedSha?: string;
  prCreatedAt?: string; 
  prNumber?: number;
  mergeDate?: string;
  hydratedCommitUrl?: string;
  proposedBranch?: string;
  promotionStatus: string;
  percent: number;
  prUrl?: string;
}

interface PRProps {
  autoExpand: boolean;
  commit: PRDetails;
}

const PrCard = ({ autoExpand, commit }: PRProps) => {

  
  let progressLabel = '';
  let iconType: 'pr' | 'progress' = 'progress';
  const percent = commit.percent;

  if (commit.promotionStatus === 'pending') {
    progressLabel = 'Opened';
    iconType = 'pr';
  } else if (commit.promotionStatus === 'promoted') {
    iconType = 'progress';
  } else if (commit.promotionStatus === 'success') {
    iconType = 'progress';
  } else if (commit.promotionStatus === 'failure') {
    progressLabel = 'Failed';
    iconType = 'progress';
  } else {
    iconType = 'progress';
  }

  const [expanded, setExpanded] = useState(autoExpand);
  const prevPromotionStatus = useRef<string>(commit.promotionStatus);

  useEffect(() => {
    if (commit.promotionStatus === 'success' && prevPromotionStatus.current !== 'success') {
      setExpanded(false);
    }
    prevPromotionStatus.current = commit.promotionStatus;
  }, [commit.promotionStatus]);

  // When autoExpand changes, update expanded state
  useEffect(() => {
    setExpanded(autoExpand);
  }, [autoExpand]);

  // Remove the card when promotionStatus is 'success'
  if ((commit.promotionStatus as string) === 'success') return null;

  return (
    <div className={`pr-card pr-card--${commit.promotionStatus}`}>
      {/* Progress Bar or PR Icon */}
      <div className="pr-card__header">
        <div style={{ position: 'relative', display: 'inline-block', width: 26, height: 26 }}>
          {iconType === 'pr' ? (
            <a href={commit.prUrl} target="_blank" rel="noopener noreferrer"
            className="pr-card__pr-icon-absolute">
              <div className="pr-card__pr-icon-absolute-text" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
              <GoGitPullRequest />
              <span className="pr-card__pr-icon-absolute-text" style={{ fontSize: 12, textDecoration: 'none' }}>#{commit.prNumber}</span>
              </div>
            </a>
          ) : (
            <CircularProgress percent={percent}/>
          )}
        </div>
        {/* CARD*/}
        <span>
          Desired SHA -&gt; {commit.proposedSha}
        </span>
        <span
          className="pr-card__toggle"
          style={{ cursor: 'pointer', position: 'absolute', right: 12, top: 12 }}
          onClick={() => setExpanded((prev) => !prev)}
          title={expanded ? 'Collapse' : 'Expand'}
        >
          {expanded ? <FiChevronUp /> : <FiChevronDown />}
        </span>
      </div>
      <div className="pr-card__progress-label">
        {progressLabel}
        {(commit.promotionStatus === 'promoted' || commit.promotionStatus === 'success') && commit.mergeDate && (
          <> Promoted ({timeAgo(commit.mergeDate)})</>
        )}

        {commit.promotionStatus === 'pending' && commit.prCreatedAt && (
          <> ({timeAgo(commit.prCreatedAt)})</>
        )}
      </div>


      {/* Show body if expanded, auto-collapse on success but allow manual toggle */}
      {expanded && (
        <div className="pr-card__body">
          {commit.prNumber && commit.prUrl && commit.promotionStatus !== 'promoted' && (
            <div className="pr-card__row">
              <div className="pr-card__label">Pull Request:</div>
              <div className="pr-card__value">
                <a
                  href={commit.prUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  title={`View PR #${commit.prNumber}`}
                >
                  <span className="env-card__value-field">PR #{commit.prNumber}</span>
                </a>
              </div>
            </div>
          )}
          <div className="pr-card__row">
            <div className="pr-card__label">Hydrated Commit:</div>
            <div className="pr-card__value">


              <a
                href={commit.hydratedCommitUrl}
                target="_blank"
                rel="noopener noreferrer"
                title={commit.hydratedSha}
              >
                <span className="env-card__value-field">{commit.hydratedSha}</span>             
              </a>
              {commit.proposedBranch && (
                <span className="env-card__value-field" style={{ marginLeft: 6 }}>
                  ({commit.proposedBranch})
                </span>
              )}
            </div>
          </div>
          <div className="pr-card__row">
            <div className="pr-card__label">Author:</div>
            <div className="pr-card__value">{commit.author}</div>
          </div>
          <div className="pr-card__row">
            <div className="pr-card__label">Message:</div>
            <div className="pr-card__value">{commit.message}</div>
          </div>


          {/* Show Merged or Opened date depending on status */}
          {commit.promotionStatus === 'promoted' && commit.mergeDate ? (
            <div className="pr-card__row">
              <div className="pr-card__label">Merged:</div>
              <div className="pr-card__value">
                {new Date(commit.mergeDate).toLocaleString()}
              </div>
            </div>
          ) : (commit.promotionStatus !== 'promoted' && commit.prCreatedAt ? (
            <div className="pr-card__row">
              <div className="pr-card__label">Opened:</div>
              <div className="pr-card__value">
                {new Date(commit.prCreatedAt).toLocaleString()}
              </div>
            </div>
          ) : null)}
          {Array.isArray(commit.checks) && commit.checks.length > 0 && (
            <div className="pr-card__checks-list">
              <div className="pr-card__row">
              <div className="pr-card__label">Checks:</div>
              </div>
              <ul>
                {commit.checks.map((check, idx) => (
                  <li key={check.name + '-' + idx} className="pr-card__check-item">
                    <span className="pr-card__check-icon pr-card__status-icon">
                      <StatusIcon phase={check.status as any} type="status" />
                    </span>
                    <span className="pr-card__check-name">{check.name}</span>
                    <div className="pr-card__check-spacer" />
                    <a href={check.details} className="pr-card__check-link" target="_blank" rel="noopener noreferrer">View details</a>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
};
export default PrCard; 