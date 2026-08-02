import React, { useMemo } from 'react';
import { Empty } from '@douyinfe/semi-ui';
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from '@douyinfe/semi-illustrations';
import CardTable from '../../common/ui/CardTable';
import { getUserUsageRankingColumns } from './UserUsageRankingColumnDefs';

const UserUsageRankingTable = ({ data }) => {
  const columns = useMemo(
    () =>
      getUserUsageRankingColumns({
        page: data.query.page,
        pageSize: data.query.pageSize,
        sortBy: data.query.sortBy,
        sortOrder: data.query.sortOrder,
        onSort: data.setSort,
        t: data.t,
      }),
    [data.query, data.setSort, data.t],
  );

  return (
    <CardTable
      columns={columns}
      dataSource={data.items}
      rowKey='user_id'
      pagination={false}
      loading={data.loading}
      scroll={{ x: 'max-content' }}
      empty={
        <Empty
          image={<IllustrationNoResult style={{ width: 150, height: 150 }} />}
          darkModeImage={
            <IllustrationNoResultDark style={{ width: 150, height: 150 }} />
          }
          description={data.t('暂无排行数据')}
          style={{ padding: 30 }}
        />
      }
    />
  );
};

export default UserUsageRankingTable;
