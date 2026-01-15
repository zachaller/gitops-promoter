import React from 'react';
import { FaSearch, FaRocket, FaInbox } from 'react-icons/fa';
import './EmptyState.scss';

export type EmptyStateType = 'no-results' | 'no-strategies' | 'no-namespace';

export interface EmptyStateProps {
  type: EmptyStateType;
  onClearFilters?: () => void;
}

export const EmptyState: React.FC<EmptyStateProps> = ({ type, onClearFilters }) => {
  const getEmptyStateContent = () => {
    switch (type) {
      case 'no-results':
        return {
          icon: <FaSearch />,
          title: 'No matching strategies found',
          description: 'Try adjusting your search or filters to find what you\'re looking for.',
          actionLabel: 'Clear Filters',
          onAction: onClearFilters,
        };

      case 'no-strategies':
        return {
          icon: <FaRocket />,
          title: 'No promotion strategies yet',
          description: 'Get started by creating your first promotion strategy to automate your GitOps workflows.',
          actionLabel: 'View Documentation',
          onAction: () => window.open('https://gitops-promoter.argoproj.io/', '_blank'),
          secondaryLabel: 'Learn More',
          onSecondaryAction: () => window.open('https://gitops-promoter.argoproj.io/getting-started/', '_blank'),
        };

      case 'no-namespace':
        return {
          icon: <FaInbox />,
          title: 'Select a namespace',
          description: 'Choose a namespace from the dropdown above to view its promotion strategies.',
          actionLabel: null,
          onAction: undefined,
        };

      default:
        return {
          icon: <FaInbox />,
          title: 'No data available',
          description: '',
          actionLabel: null,
          onAction: undefined,
        };
    }
  };

  const content = getEmptyStateContent();

  return (
    <div className="empty-state">
      <div className="empty-state__icon">{content.icon}</div>
      <h2 className="empty-state__title">{content.title}</h2>
      <p className="empty-state__description">{content.description}</p>

      {content.actionLabel && content.onAction && (
        <div className="empty-state__actions">
          <button className="empty-state__button" onClick={content.onAction}>
            {content.actionLabel}
          </button>
          {content.secondaryLabel && content.onSecondaryAction && (
            <button
              className="empty-state__button empty-state__button--secondary"
              onClick={content.onSecondaryAction}
            >
              {content.secondaryLabel}
            </button>
          )}
        </div>
      )}
    </div>
  );
};
