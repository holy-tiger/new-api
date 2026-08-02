import React from 'react';
import { Button } from '@douyinfe/semi-ui';
import { ArrowDown, ArrowUp } from 'lucide-react';
import { renderNumber, renderQuota } from '../../../helpers';
import { getUsageRankingPosition } from '../../../helpers/userUsageRanking';

const SortHeader = ({ label, field, sortBy, sortOrder, onSort }) => {
  const active = sortBy === field;
  const icon = active ? (
    sortOrder === 'desc' ? (
      <ArrowDown size={14} />
    ) : (
      <ArrowUp size={14} />
    )
  ) : null;
  return (
    <Button
      theme='borderless'
      type='tertiary'
      size='small'
      icon={icon}
      iconPosition='right'
      onClick={() => onSort(field)}
    >
      {label}
    </Button>
  );
};

export const getUserUsageRankingColumns = ({
  page,
  pageSize,
  sortBy,
  sortOrder,
  onSort,
  t,
}) => [
  {
    title: t('排名'),
    key: 'rank',
    render: (text, record, index) =>
      getUsageRankingPosition(page, pageSize, index),
  },
  { title: t('用户名'), dataIndex: 'username' },
  {
    title: t('显示名称'),
    dataIndex: 'display_name',
    render: (value) => value || '-',
  },
  {
    title: (
      <SortHeader
        label={t('累计 Token 使用量')}
        field='token_used'
        sortBy={sortBy}
        sortOrder={sortOrder}
        onSort={onSort}
      />
    ),
    dataIndex: 'token_used',
    render: (value) => renderNumber(value || 0),
  },
  {
    title: (
      <SortHeader
        label={t('计费金额')}
        field='quota'
        sortBy={sortBy}
        sortOrder={sortOrder}
        onSort={onSort}
      />
    ),
    dataIndex: 'quota',
    render: (value) => renderQuota(value || 0, 4),
  },
  {
    title: (
      <SortHeader
        label={t('调用次数')}
        field='count'
        sortBy={sortBy}
        sortOrder={sortOrder}
        onSort={onSort}
      />
    ),
    dataIndex: 'count',
    render: (value) => renderNumber(value || 0),
  },
];
