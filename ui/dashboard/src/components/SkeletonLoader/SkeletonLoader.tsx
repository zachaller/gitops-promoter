import React from 'react';
import './SkeletonLoader.scss';

export const SkeletonTile: React.FC = () => {
  return (
    <div className="ps-tile skeleton-tile">
      <div className="ps-tile__header">
        <div className="skeleton skeleton-icon"></div>
        <div className="skeleton skeleton-title"></div>
      </div>

      <div className="ps-tile__row">
        <div className="skeleton skeleton-label"></div>
        <div className="skeleton skeleton-text"></div>
      </div>
      <div className="ps-tile__row">
        <div className="skeleton skeleton-label"></div>
        <div className="skeleton skeleton-status-icon"></div>
      </div>
      <div className="ps-tile__row">
        <div className="skeleton skeleton-label"></div>
        <div className="skeleton skeleton-text"></div>
      </div>
      <div className="ps-tile__row">
        <div className="skeleton skeleton-label"></div>
      </div>
      <div className="ps-tile__envs">
        <div className="ps-tile__env-row-grid">
          <div className="skeleton skeleton-env-text"></div>
          <div className="skeleton skeleton-env-icon"></div>
        </div>
        <div className="ps-tile__env-row-grid">
          <div className="skeleton skeleton-env-text"></div>
          <div className="skeleton skeleton-env-icon"></div>
        </div>
        <div className="ps-tile__env-row-grid">
          <div className="skeleton skeleton-env-text"></div>
          <div className="skeleton skeleton-env-icon"></div>
        </div>
      </div>
    </div>
  );
};

export const SkeletonLoader: React.FC<{ count?: number }> = ({ count = 3 }) => {
  return (
    <div className="applications-tiles">
      {Array.from({ length: count }).map((_, idx) => (
        <SkeletonTile key={idx} />
      ))}
    </div>
  );
};
