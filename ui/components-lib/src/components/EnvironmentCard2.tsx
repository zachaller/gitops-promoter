import React, { useState, useEffect } from 'react';
import { FaServer, FaCheckCircle, FaHourglassHalf, FaChevronUp, FaChevronDown, FaTimesCircle } from 'react-icons/fa';
import './EnvironmentCard.scss';
import CommitCard from './CommitCard';
import { StatusIcon } from './StatusIcon';

// Define your environments and checks as data
const environments = [
  {
    name: 'development',
    display: 'DEVELOPMENT',
    commit: {
      sha: 'b9587255',
      link: 'https://github.com/example/repo/commit/b9587255',
      author: 'Shirley Huang',
      message: 'Update kustomization.yaml',
      mergeDate: '2025-05-23T10:48:29Z',
      autoMerge: false,
      lastSync: '2025-08-19T12:30:04Z',
    },
    checks: [
      { name: 'Build', details: '#' },
      { name: 'Lint', details: '#' },
      { name: 'Unit Tests', details: '#' },
      { name: 'Deploy', details: '#' },
    ],
  },
  {
    name: 'staging',
    display: 'STAGING',
    commit: {
      sha: 'stag5678efgh',
      link: 'https://github.com/example/repo/commit/stag5678efgh',
      author: 'Bob Stager',
      message: 'fix: update API endpoint for staging',
      mergeDate: '2025-05-23T10:48:29Z',
      autoMerge: true,
      lastSync: '2025-08-19T12:30:04Z',
    },
    checks: [
      { name: 'Build', details: '#' },
      { name: 'Lint', details: '#' },
      { name: 'Unit Tests', details: '#' },
      { name: 'Deploy', details: '#' },
    ],
  },
  {
    name: 'production',
    display: 'PRODUCTION',
    commit: {
      sha: 'prod5678efgh',
      link: 'https://github.com/example/repo/commit/prod5678efgh',
      author: 'Bob Stager',
      message: 'fix: update API endpoint for production',
      mergeDate: '2025-05-23T10:48:29Z',
      autoMerge: true,
      lastSync: '2025-08-19T12:30:04Z',
    },
    checks: [
      { name: 'Build', details: '#' },
      { name: 'Lint', details: '#' },
      { name: 'Unit Tests', details: '#' },
      { name: 'Deploy', details: '#' },
    ],
  },
  
];

const statusLabel = (phase: string) => {
  switch (phase) {
    case 'success':
      return 'Healthy';
    case 'pending':
      return 'Pending';
    case 'failure':
      return 'Failed';
    default:
      return 'Unknown';
  }
};

const borderClass = (phase: string) => {
  switch (phase) {
    case 'success':
      return 'env-card--success';
    case 'pending':
      return 'env-card--pending';
    case 'failure':
      return 'env-card--failure';
    default:
      return 'env-card--unknown';
  }
};

const EnvironmentCard2: React.FC = () => {
  // State for each environment's checks, progress, and status
  const [checks, setChecks] = useState(
    environments.map(env => env.checks.map(check => ({ ...check, status: 'Pending' })))
  );
  const [checksInProgress, setChecksInProgress] = useState(environments.map(() => 0));
  const [statuses, setStatuses] = useState(environments.map(() => 'pending'));
  
  // New state to track which cards are expanded (can be multiple)
  const [expandedIdxs, setExpandedIdxs] = useState<number[]>([]);
  // New state to track which cards are explicitly closed
  const [closedIdxs, setClosedIdxs] = useState<number[]>([]);

  useEffect(() => {
    let interval: ReturnType<typeof setInterval>;
    function startEnvAnimation(idx: number) {
      let step = 0;
      setStatuses(prev => prev.map((s, i) => (i === idx ? 'progressing' : s)));
      const stopAt = environments[idx].checks.length;
      interval = setInterval(() => {
        if (step < stopAt) {
          setChecks(prev => prev.map((arr, i) =>
            i === idx
              ? arr.map((c, ci) =>
                  // For the last environment, fail the last check
                  idx === environments.length - 1 && ci === stopAt - 1 && step === stopAt - 1
                    ? { ...c, status: 'Failure' }
                    : ci < step ? { ...c, status: 'Success' } : c
                )
              : arr
          ));
          setChecksInProgress(prev => prev.map((v, i) => (i === idx ? step : v)));
          step++;
          if (step === stopAt) {
            setChecksInProgress(prev => prev.map((v, i) => (i === idx ? stopAt : v)));
            if (idx === environments.length - 1) {
              // Set last check to failure and environment to failure, and do not update checks or status after this
              setChecks(prev => prev.map((arr, i) =>
                i === idx
                  ? arr.map((c, ci) =>
                      ci < stopAt - 1 ? { ...c, status: 'Success' } :
                      ci === stopAt - 1 ? { ...c, status: 'Failure' } : c
                    )
                  : arr
              ));
              setStatuses(prev => prev.map((s, i) => (i === idx ? 'failure' : s)));
            } else {
              setStatuses(prev => prev.map((s, i) => (i === idx ? 'success' : s)));
              if (idx + 1 < environments.length) {
                setTimeout(() => startEnvAnimation(idx + 1), 1000);
              }
            }
            clearInterval(interval);
          }
        }
      }, 1000);
    }
    startEnvAnimation(0);
    return () => clearInterval(interval);
  }, []);

  useEffect(() => {
    // When an environment becomes 'success', always hide its commit card
    statuses.forEach((status, idx) => {
      if (status === 'success') {
        setExpandedIdxs(prev => prev.filter(i => i !== idx));
        setClosedIdxs(prev => prev.filter(i => i !== idx));
      }
    });
  }, [statuses]);

  return (
    <div className="env-cards-container">
      {environments.map((env, idx) => {
        const isSuccess = statuses[idx] === 'success';
        const isFailure = statuses[idx] === 'failure';
        const hasFailure = checks[idx].some(c => c.status === 'Failure');
        const commitStatus = isFailure || hasFailure ? 'failure' : isSuccess ? 'success' : 'pending';
        const commitData = {
          ...env.commit,
          checks: checks[idx],
          checksInProgress: checksInProgress[idx],
          totalChecks: env.checks.length,
          status: commitStatus,
        };
        const isFirstNotSuccess = statuses.findIndex(s => s !== 'success') === idx;
        const isExpanded = expandedIdxs.includes(idx);
        const isClosed = closedIdxs.includes(idx);
        // Show commit card if toggled open, or if running and not closed
        const showCommitCard = isExpanded || (isFirstNotSuccess && !isClosed);
        return (
          <div key={env.name} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', position: 'relative' }}>
            <div
              className={`env-card ${borderClass(commitStatus)}`}
              style={{ cursor: (commitStatus === 'pending') ? 'default' : 'pointer', position: 'relative' }}
              onClick={e => {
                // Prevent toggling when clicking a link inside the card
                if ((e.target as HTMLElement).tagName === 'A') return;
                if (showCommitCard) {
                  setClosedIdxs(prev => prev.includes(idx) ? prev : [...prev, idx]);
                  setExpandedIdxs(prev => prev.filter(i => i !== idx));
                } else {
                  setClosedIdxs(prev => prev.filter(i => i !== idx));
                  setExpandedIdxs(prev => prev.includes(idx) ? prev : [...prev, idx]);
                }
              }}
            >
              {/* Chevron icon in top right of the environment card */}
              <span
                style={{ position: 'absolute', right: 16, top: 12, fontSize: 20, zIndex: 2 }}
              >
                {showCommitCard ? <FaChevronUp /> : <FaChevronDown />}
              </span>
              <div className="env-card__header">
                <FaServer className="env-card__icon" />
                <span className="env-card__env-name">{env.display}</span>
              </div>
              <div className="env-card__status-row">
                <StatusIcon phase={commitStatus} type="health" />
                <span className={`env-card__status-label env-card__status-label--${commitStatus}`}>{statusLabel(commitStatus)}</span>
              </div>
              <div className="env-card__field-group">
                <div className="env-card__row"><span className="env-card__label">Commit SHA:</span> <a className="env-card__value env-card__commit-link" href={commitData.link} target="_blank" rel="noopener noreferrer">#{commitData.sha}</a></div>
                <div className="env-card__row"><span className="env-card__label">Auto Merge:</span> <span className="env-card__value">{String(commitData.autoMerge)}</span></div>
                <div className="env-card__row"><span className="env-card__label">Author:</span> <span className="env-card__value">{commitData.author}</span></div>
                <div className="env-card__row"><span className="env-card__label">Merge Date:</span> <span className="env-card__value">{commitData.mergeDate ? new Date(commitData.mergeDate).toLocaleString() : '-'}</span></div>
                <div className="env-card__row"><span className="env-card__label">Message:</span> <span className="env-card__value">{commitData.message}</span></div>
                <div className="env-card__row"><span className="env-card__label">Last Sync:</span> <span className="env-card__value">{commitData.lastSync}</span></div>
              </div>
            </div>
            {/* Show commit card if toggled open, or if running and not closed */}
            {showCommitCard && (
              <CommitCard expanded={true} commit={commitData} />
            )}
          </div>
        );
      })}
    </div>
  );
};

export default EnvironmentCard2; 