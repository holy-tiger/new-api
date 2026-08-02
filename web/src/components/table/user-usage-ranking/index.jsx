import React, { useContext } from 'react';
import { Banner } from '@douyinfe/semi-ui';
import { StatusContext } from '../../../context/Status';
import { useIsMobile } from '../../../hooks/common/useIsMobile';
import { useUserUsageRankingData } from '../../../hooks/user-usage-ranking/useUserUsageRankingData';
import { createCardProPagination } from '../../../helpers/utils';
import CardPro from '../../common/ui/CardPro';
import UserUsageRankingFilters from './UserUsageRankingFilters';
import UserUsageRankingTable from './UserUsageRankingTable';

const UserUsageRankingPage = () => {
  const [statusState] = useContext(StatusContext);
  const enabled = statusState?.status?.enable_data_export === true;
  const data = useUserUsageRankingData(enabled);
  const isMobile = useIsMobile();

  if (!enabled) {
    return (
      <Banner
        type='warning'
        description={data.t('数据看板统计未启用')}
        fullMode={false}
      />
    );
  }

  return (
    <CardPro
      type='type1'
      descriptionArea={
        <div>
          <div className='text-lg font-semibold'>
            {data.t('用户使用量排行')}
          </div>
          <div className='text-sm text-gray-500'>
            {data.t('统计数据按小时汇总，可能存在约 5 分钟延迟')}
          </div>
        </div>
      }
      actionsArea={
        <UserUsageRankingFilters
          range={data.query.range}
          onChange={data.setRange}
          t={data.t}
        />
      }
      paginationArea={createCardProPagination({
        currentPage: data.query.page,
        pageSize: data.query.pageSize,
        total: data.total,
        onPageChange: data.setPage,
        onPageSizeChange: data.setPageSize,
        pageSizeOpts: [20, 50, 100],
        isMobile,
        t: data.t,
      })}
      t={data.t}
    >
      <UserUsageRankingTable data={data} />
    </CardPro>
  );
};

export default UserUsageRankingPage;
