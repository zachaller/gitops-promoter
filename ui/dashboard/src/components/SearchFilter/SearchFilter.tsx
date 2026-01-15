import React, { useState, useMemo } from 'react';
import { FaSearch, FaFilter, FaTimes } from 'react-icons/fa';
import type { StatusType } from '@lib/components/StatusIcon';
import './SearchFilter.scss';

export interface SearchFilterProps {
  searchQuery: string;
  onSearchChange: (query: string) => void;
  statusFilter: StatusType | 'all';
  onStatusFilterChange: (status: StatusType | 'all') => void;
  resultCount?: number;
  totalCount?: number;
}

export const SearchFilter: React.FC<SearchFilterProps> = ({
  searchQuery,
  onSearchChange,
  statusFilter,
  onStatusFilterChange,
  resultCount,
  totalCount,
}) => {
  const [showFilters, setShowFilters] = useState(false);

  const handleClearSearch = () => {
    onSearchChange('');
  };

  const handleClearFilters = () => {
    onSearchChange('');
    onStatusFilterChange('all');
  };

  const hasActiveFilters = searchQuery !== '' || statusFilter !== 'all';

  const statusOptions: Array<{ value: StatusType | 'all'; label: string; color: string }> = [
    { value: 'all', label: 'All Statuses', color: '#6D7F8B' },
    { value: 'promoted', label: 'Promoted', color: '#18BE94' },
    { value: 'pending', label: 'Pending', color: '#0DADEA' },
    { value: 'failure', label: 'Failed', color: '#E96D76' },
    { value: 'unknown', label: 'Unknown', color: '#B3B3B3' },
  ];

  return (
    <div className="search-filter">
      <div className="search-filter__main">
        <div className="search-filter__search-box">
          <FaSearch className="search-filter__search-icon" />
          <input
            type="text"
            className="search-filter__input"
            placeholder="Search promotion strategies..."
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
          />
          {searchQuery && (
            <button
              className="search-filter__clear-btn"
              onClick={handleClearSearch}
              aria-label="Clear search"
            >
              <FaTimes />
            </button>
          )}
        </div>

        <button
          className={`search-filter__filter-toggle ${showFilters ? 'active' : ''}`}
          onClick={() => setShowFilters(!showFilters)}
          aria-label="Toggle filters"
        >
          <FaFilter />
          <span>Filters</span>
          {hasActiveFilters && <span className="search-filter__filter-badge"></span>}
        </button>

        {hasActiveFilters && (
          <button
            className="search-filter__clear-all"
            onClick={handleClearFilters}
          >
            Clear All
          </button>
        )}
      </div>

      {showFilters && (
        <div className="search-filter__filters">
          <div className="search-filter__filter-group">
            <label className="search-filter__filter-label">Status:</label>
            <div className="search-filter__status-buttons">
              {statusOptions.map((option) => (
                <button
                  key={option.value}
                  className={`search-filter__status-btn ${
                    statusFilter === option.value ? 'active' : ''
                  }`}
                  onClick={() => onStatusFilterChange(option.value)}
                  style={{
                    borderColor: statusFilter === option.value ? option.color : undefined,
                    color: statusFilter === option.value ? option.color : undefined,
                  }}
                >
                  <span
                    className="search-filter__status-indicator"
                    style={{ backgroundColor: option.color }}
                  ></span>
                  {option.label}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

      {resultCount !== undefined && totalCount !== undefined && (
        <div className="search-filter__results">
          Showing {resultCount} of {totalCount} promotion strategies
        </div>
      )}
    </div>
  );
};
