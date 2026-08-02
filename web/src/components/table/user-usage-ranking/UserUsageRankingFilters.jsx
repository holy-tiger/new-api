import React from 'react';
import { DatePicker } from '@douyinfe/semi-ui';
import { DATE_RANGE_PRESETS } from '../../../constants/console.constants';

const allowedPresets = new Set(['今天', '近 7 天', '近 30 天', '本月']);

const UserUsageRankingFilters = ({ range, onChange, t }) => (
  <div className='w-full md:w-[520px]'>
    <DatePicker
      className='w-full'
      type='dateTimeRange'
      value={range}
      onChange={(value) => {
        if (Array.isArray(value) && value.length === 2) onChange(value);
      }}
      placeholder={[t('开始时间'), t('结束时间')]}
      presets={DATE_RANGE_PRESETS.filter((preset) =>
        allowedPresets.has(preset.text),
      ).map((preset) => ({
        text: t(preset.text),
        start: preset.start(),
        end: preset.end(),
      }))}
      showClear={false}
    />
  </div>
);

export default UserUsageRankingFilters;
