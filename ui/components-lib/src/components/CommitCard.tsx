import { useState } from 'react';
import './CommitCard.scss';
import CircularProgress from './CircularProgress';
import { StatusIcon } from './StatusIcon';


interface CommitCheck {
  name: string;
  status: string;
  details: string; //Link here
}

interface CommitDetails {
  sha: string;
  link: string;
  author: string;
  message: string;
  checks: CommitCheck[];
  checksInProgress: number;
  totalChecks: number;
  status: string;
}

interface CommitCardProps {
  expanded: boolean;
  commit: CommitDetails;
  hideOnSuccess: boolean;
}

const allSuccessChecks = (checks: CommitCheck[]) => {
  return checks.every(check => check.status === 'success');
}


const CommitCard = ({commit}: CommitCardProps) => {

  const passed = commit.checks.filter(check => check.status === 'success').length;
  const percent = commit.totalChecks > 0 ? (passed/ commit.totalChecks) * 100 : 0;

  
  return (
    <div className={`commit-card commit-card--${commit.status || 'unknown'}`}>



    {/* Progress Bar*/}
    <div className="commit-card__header">
    <CircularProgress percent = {percent} />


      <span>  
        <a href = {commit.link}
        target = "_blank"
        rel = "noopener noreferrer">  
          Commit: {commit.sha} </a> 
      </span>
      </div>

      <div className="commit-card__progress-label">
        {commit.totalChecks === 0
          ? 'No checks'
          : commit.checksInProgress === 0 && allSuccessChecks(commit.checks)
            ? `${commit.totalChecks} of ${commit.totalChecks} checks passed`
            : `${commit.checksInProgress} of ${commit.totalChecks} checks in progress`}
      </div>

    <div className="commit-card__body">
      <div className="commit-card__row">
        <div className="commit-card__label">Author:</div>
        <div className="commit-card__value">{commit.author}</div>
      </div>
      <div className="commit-card__row">
        <div className="commit-card__label">Message:</div>
        <div className="commit-card__value">{commit.message}</div>
      </div>

      {Array.isArray(commit.checks) && commit.checks.length > 0 && (
        <div className="commit-card__checks-list">
          <div className="commit-card__label">Checks:</div>
          <ul>
            {commit.checks.map((check) => (
              <li key={check.name} className="commit-card__check-item">
                <span className="commit-card__check-icon">
                  <StatusIcon phase={check.status as any} type="status" />
                </span>
                <span className="commit-card__check-name">{check.name}</span>
                <div className="commit-card__check-spacer" />
                <a href={check.details} className="commit-card__check-link" target="_blank" rel="noopener noreferrer">View details</a>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  </div>
);
};
export default CommitCard; 