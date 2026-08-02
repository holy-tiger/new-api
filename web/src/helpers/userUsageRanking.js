import dayjs from 'dayjs';

export const MAX_USER_USAGE_RANGE_MS = 366 * 24 * 60 * 60 * 1000;

export const createInitialUsageRankingState = (now = new Date()) => ({
  range: [
    dayjs(now).subtract(6, 'day').startOf('day').toDate(),
    dayjs(now).endOf('day').toDate(),
  ],
  page: 1,
  pageSize: 20,
  sortBy: 'token_used',
  sortOrder: 'desc',
});

export const isValidUsageRankingRange = (range) => {
  if (!Array.isArray(range) || range.length !== 2) return false;
  const start = new Date(range[0]).getTime();
  const end = new Date(range[1]).getTime();
  return (
    Number.isFinite(start) &&
    Number.isFinite(end) &&
    end >= start &&
    end - start <= MAX_USER_USAGE_RANGE_MS
  );
};

export const usageRankingReducer = (state, action) => {
  if (action.type === 'range') {
    return { ...state, range: action.range, page: 1 };
  }
  if (action.type === 'page') {
    return { ...state, page: action.page };
  }
  if (action.type === 'page_size') {
    return { ...state, page: 1, pageSize: action.pageSize };
  }
  if (action.type === 'sort') {
    return {
      ...state,
      page: 1,
      sortBy: action.field,
      sortOrder:
        state.sortBy === action.field && state.sortOrder === 'desc'
          ? 'asc'
          : 'desc',
    };
  }
  return state;
};

export const toUsageRankingParams = (state) => ({
  start_timestamp: Math.floor(new Date(state.range[0]).getTime() / 1000),
  end_timestamp: Math.floor(new Date(state.range[1]).getTime() / 1000),
  page: state.page,
  page_size: state.pageSize,
  sort_by: state.sortBy,
  sort_order: state.sortOrder,
});

export const getUsageRankingPosition = (page, size, index) =>
  (page - 1) * size + index + 1;
