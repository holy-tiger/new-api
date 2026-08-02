import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { API, showError } from '../../helpers';
import {
  createInitialUsageRankingState,
  isValidUsageRankingRange,
  toUsageRankingParams,
  usageRankingReducer,
} from '../../helpers/userUsageRanking';

export const useUserUsageRankingData = (enabled) => {
  const { t } = useTranslation();
  const [query, dispatch] = useReducer(
    usageRankingReducer,
    undefined,
    createInitialUsageRankingState,
  );
  const [items, setItems] = useState([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const requestSequence = useRef(0);

  const load = useCallback(async (currentQuery) => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    try {
      const res = await API.get('/api/data/user-ranking', {
        params: toUsageRankingParams(currentQuery),
      });
      if (sequence !== requestSequence.current) return;
      const { success, message, data } = res.data;
      if (!success) {
        showError(message);
        return;
      }
      setItems(data.items || []);
      setTotal(data.total || 0);
    } catch (error) {
      if (sequence === requestSequence.current) {
        showError(error.message);
      }
    } finally {
      if (sequence === requestSequence.current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    if (enabled) {
      load(query);
    }
  }, [enabled, load, query]);

  const setRange = useCallback(
    (range) => {
      if (!isValidUsageRankingRange(range)) {
        showError(t('时间范围不能超过 366 天'));
        return;
      }
      dispatch({ type: 'range', range });
    },
    [t],
  );

  const setPage = useCallback((page) => {
    dispatch({ type: 'page', page });
  }, []);
  const setPageSize = useCallback((pageSize) => {
    dispatch({ type: 'page_size', pageSize });
  }, []);
  const setSort = useCallback((field) => {
    dispatch({ type: 'sort', field });
  }, []);

  return {
    t,
    query,
    items,
    total,
    loading,
    setRange,
    setPage,
    setPageSize,
    setSort,
  };
};
