import { describe, expect, test } from 'bun:test';
import {
  createInitialUsageRankingState,
  getUsageRankingPosition,
  isValidUsageRankingRange,
  toUsageRankingParams,
  usageRankingReducer,
} from './userUsageRanking';

describe('user usage ranking state', () => {
  test('uses seven calendar days, page 20, and Token descending by default', () => {
    const now = new Date('2026-08-02T12:00:00+08:00');
    const state = createInitialUsageRankingState(now);
    expect(state).toMatchObject({
      page: 1,
      pageSize: 20,
      sortBy: 'token_used',
      sortOrder: 'desc',
    });
    expect(new Date(state.range[0]).getDate()).toBe(27);
    expect(new Date(state.range[1]).getDate()).toBe(2);
    expect(toUsageRankingParams(state)).toMatchObject({
      page: 1,
      page_size: 20,
      sort_by: 'token_used',
      sort_order: 'desc',
    });
  });

  test('sorting and page-size changes reset page one', () => {
    const initial = { ...createInitialUsageRankingState(), page: 4 };
    const sorted = usageRankingReducer(initial, {
      type: 'sort',
      field: 'quota',
    });
    expect(sorted).toMatchObject({
      page: 1,
      sortBy: 'quota',
      sortOrder: 'desc',
    });
    const reversed = usageRankingReducer(sorted, {
      type: 'sort',
      field: 'quota',
    });
    expect(reversed.sortOrder).toBe('asc');
    expect(
      usageRankingReducer(reversed, { type: 'page_size', pageSize: 100 }),
    ).toMatchObject({ page: 1, pageSize: 100 });
  });

  test('validates range and computes cross-page ranking', () => {
    expect(
      isValidUsageRankingRange([
        new Date('2026-01-01T00:00:00Z'),
        new Date('2026-12-31T00:00:00Z'),
      ]),
    ).toBe(true);
    expect(
      isValidUsageRankingRange([
        new Date('2025-01-01T00:00:00Z'),
        new Date('2026-12-31T00:00:00Z'),
      ]),
    ).toBe(false);
    expect(getUsageRankingPosition(3, 20, 4)).toBe(45);
  });
});
