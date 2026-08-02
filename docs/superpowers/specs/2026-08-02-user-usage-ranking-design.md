# 用户使用量排行设计

## 目标

新增一个管理员专用的“用户使用量排行”页面，按选定时间范围统计每个有效用户的累计 Token 使用量、计费金额和调用次数。页面使用服务端聚合、排序和分页，避免复用数据看板的全量明细接口后在浏览器内计算。选定时间范围内没有使用量的启用用户也进入排行，三个用量字段显示为 0；禁用或已软删除的用户不进入排行。

页面提供所有匹配用户的完整排行，不把结果硬限制为 100 人。选择每页 100 条时，第一页即为当前排序下的 Top 100。

## 范围

本次包含：

- 新增管理员排行查询接口。
- 新增管理员页面、路由和侧边栏入口。
- 支持时间范围、远程排序和分页。
- 按用户 ID 聚合，并显示当前用户名和显示名称。
- 包含所有启用且未软删除的普通用户、管理员和 Root；所选范围内没有使用量时补 0。
- 排除禁用和已软删除的用户，即使他们在所选范围内存在历史用量记录。
- 增加所需的前端国际化文案。

本次不包含：

- 修改 `quota_data` 的采集和落库机制。
- 改变现有数据看板中的用户排行图表。
- 增加排行缓存、Redis 榜单或新的汇总表。
- 拆分输入 Token 与输出 Token。现有 `quota_data` 只保存二者之和。
- 把计费金额解释为用户实际支付的现金。

## 统计口径

数据源继续使用 `quota_data`。

- 累计 Token 使用量：`SUM(quota_data.token_used)`。
- 计费额度：`SUM(quota_data.quota)`。
- 调用次数：`SUM(quota_data.count)`。
- 时间条件：`created_at >= start_timestamp AND created_at <= end_timestamp`。

`token_used` 是请求的输入 Token 与输出 Token 之和。`quota` 是请求结算得到的内部计费额度，已体现模型价格或倍率、输入输出差异、分组倍率等计费规则。前端使用现有 `renderQuota()` 将它显示为系统配置的 USD、CNY 或自定义货币。该金额是使用成本的等价值，可能由钱包、赠送额度、兑换额度或订阅额度承担，不等同于用户实际现金支出。

现有数据按整点小时桶统计，并默认每 5 分钟从进程内缓存落库。因此页面需要提示“统计数据按小时汇总，可能存在约 5 分钟延迟”。自定义时间的边界精度也以小时桶为准。

排行依赖 `DataExportEnabled`。该配置关闭时，前端显示“数据看板统计未启用”的明确提示，不把缺少统计数据表现为普通空排行；接口不尝试改用 `users.used_quota` 或消费日志临时重算。

## API 设计

### 路由与权限

新增接口：

```http
GET /api/data/user-ranking
```

路由使用 `middleware.AdminAuth()`，与现有 `/api/data/users` 保持相同的管理员权限要求。前端路由也使用 `AdminRoute`，但后端权限校验仍是安全边界。

### 请求参数

| 参数 | 类型 | 必填 | 默认值 | 规则 |
| --- | --- | --- | --- | --- |
| `start_timestamp` | int64 | 是 | 无 | Unix 秒 |
| `end_timestamp` | int64 | 是 | 无 | Unix 秒，必须不小于起始时间 |
| `page` | int | 否 | 1 | 必须大于 0 |
| `page_size` | int | 否 | 20 | 只允许 20、50、100 |
| `sort_by` | string | 否 | `token_used` | 只允许 `token_used`、`quota`、`count` |
| `sort_order` | string | 否 | `desc` | 只允许 `asc`、`desc` |

自定义时间跨度最多 366 天。后端必须执行该校验，不能只依赖前端日期组件。

排序参数必须通过固定白名单映射到 SQL 表达式，禁止直接把请求参数拼入 `ORDER BY`。主排序值相同时，追加 `user_id ASC`，保证分页结果稳定。

### 响应

```json
{
  "success": true,
  "message": "",
  "data": {
    "items": [
      {
        "user_id": 123,
        "username": "alice",
        "display_name": "Alice Zhang",
        "token_used": 1200000,
        "quota": 850000,
        "count": 268
      }
    ],
    "total": 100,
    "page": 1,
    "page_size": 20
  }
}
```

没有符合条件的启用用户时返回空 `items` 和 `total: 0`，不是错误。只要存在符合条件的启用用户，即使所有人的用量都是 0，也返回这些用户。参数错误返回明确消息；数据库错误使用项目现有的 `common.ApiError` 路径。

## 后端数据访问设计

在 `model/usedata.go` 增加专用查询函数和结果 DTO。查询分为两个逻辑阶段：

1. 在时间范围内按 `quota_data.user_id` 聚合 Token、计费额度和调用次数。
2. 从启用且未软删除的 `users` 表出发，左连接聚合结果；缺少聚合记录的字段补 0，然后排序和分页。

结果 DTO 中的 `token_used`、`quota`、`count` 和总数使用 `int64`，避免长时间范围或高用量场景下累计值溢出。分页页码和每页数量继续使用 `int`。

推荐的逻辑结构为：

```sql
SELECT
  users.id AS user_id,
  users.username,
  COALESCE(users.display_name, '') AS display_name,
  COALESCE(aggregated.token_used, 0) AS token_used,
  COALESCE(aggregated.quota, 0) AS quota,
  COALESCE(aggregated.count, 0) AS count
FROM users
LEFT JOIN (
  SELECT
    user_id,
    SUM(token_used) AS token_used,
    SUM(quota) AS quota,
    SUM(count) AS count
  FROM quota_data
  WHERE created_at >= ? AND created_at <= ?
  GROUP BY user_id
) AS aggregated ON aggregated.user_id = users.id
WHERE users.status = 1 AND users.deleted_at IS NULL
ORDER BY COALESCE(aggregated.token_used, 0) DESC, users.id ASC
LIMIT ? OFFSET ?
```

上例展示默认排序。实际查询根据经过白名单校验的 `sort_by` 和 `sort_order`，在三个 `COALESCE` 聚合表达式和两个固定方向中选择排序表达式。总数直接统计 `status = 1` 且 `deleted_at IS NULL` 的用户。使用 GORM 参数绑定和子查询能力实现；SQL 只使用 SQLite、MySQL 5.7.8+ 和 PostgreSQL 9.6 均支持的 `SUM`、`COALESCE`、`GROUP BY`、`LEFT JOIN`、`LIMIT` 和 `OFFSET`。

按 `user_id` 聚合可避免用户改名后被拆成多个排行项。用户表是排行主体，因此用户名和显示名称始终使用当前值。角色不参与过滤：普通用户、管理员和 Root 都进入排行。禁用用户、软删除用户和物理删除用户都不进入排行，他们的历史用量也不会出现在结果或总数中。

聚合子查询不是应用创建的物理临时表。数据库会扫描所选时间范围内的 `quota_data` 并按用户分组，随后与有效用户集合连接。准确的全局排序本来就需要计算范围内各用户的汇总值；与原查询相比，新增成本主要是把零用量有效用户加入排序。在当前规模下无需增加缓存或汇总表，后续数据量显著增长时再评估 `(created_at, user_id)` 复合索引。

## 前端页面设计

新增管理员页面路由：

```text
/console/user-usage-ranking
```

页面只包含时间筛选、表格和分页器，不增加图表或统计卡片。

页面加载时读取现有状态信息中的 `enable_data_export`。该值不是 `true` 时，页面显示统计未启用提示并且不发起排行查询。后端接口仍独立进行权限和参数校验，不能依赖前端隐藏或提示保证安全。

### 时间筛选

提供以下快捷范围：

- 今天
- 近 7 天
- 近 30 天
- 本月
- 自定义

自定义范围最多 366 天。初次进入默认选择近 7 天。时间改变后回到第一页并重新请求。

### 表格

| 列 | 数据 | 排序 |
| --- | --- | --- |
| 排名 | `(page - 1) * page_size + 当前行序号 + 1` | 否 |
| 用户名 | `username` | 否 |
| 显示名称 | `display_name` | 否 |
| 累计 Token 使用量 | `token_used` | 服务端升降序 |
| 计费金额 | `quota`，使用 `renderQuota()` 显示 | 服务端升降序 |
| 调用次数 | `count` | 服务端升降序 |

默认按累计 Token 使用量降序。点击可排序列表头时更新 `sort_by` 和 `sort_order`、将页码重置为 1，并重新查询。分页大小允许 20、50、100，改变每页数量时也回到第一页。

页面需要处理加载中、空结果和请求失败状态。重新查询期间保留当前表格内容并显示加载状态，成功后整体替换结果；失败时保留原结果并显示统一错误消息。快速连续改变条件时使用请求序号，只应用最后一次请求结果，旧请求不能覆盖较新的结果。

## 前端入口与配置

- 在 `web/src/App.jsx` 注册懒加载页面，并用 `AdminRoute` 包裹。
- 在 `web/src/components/layout/SiderBar.jsx` 的 `routerMap` 和管理员菜单中加入“用户使用量排行”。
- 在管理员侧边栏全局配置默认值、重置值和模块说明中加入同一模块键，避免新入口因配置系统缺少键而不可管理。
- 新增中文源字符串，并同步 `web/src/i18n/locales/` 下各语言文件；运行现有 i18n 工具检查。

建议模块键统一使用 `user_usage_ranking`，避免路由、菜单和侧边栏配置使用不同名称。

## 排名语义

排名随当前排序字段和方向变化，采用顺序排名而不是并列排名。例如两个用户的 Token 使用量相同，仍显示第 4、5 名。升序时第 1 名表示当前排序下数值最小的用户。跨页排名连续。

零用量用户参与相同排序：降序时排在正用量用户之后，升序时排在正用量用户之前；多个零用量用户之间按 `user_id ASC` 稳定排序。

## 错误处理

- 非管理员请求由认证中间件拒绝。
- 时间戳无法解析、时间顺序错误、跨度超限、页码无效、分页大小非法或排序参数非法时，不执行数据库查询。
- 没有任何启用且未软删除用户时正常显示空状态；存在零用量启用用户时显示 0 值排行而不是空状态。
- `DataExportEnabled` 关闭时，前端显示统计未启用提示，不显示容易误解的普通空结果。
- 前端重新查询时保留现有结果并显示加载状态；请求失败时展示项目统一错误消息，不使用部分或过期响应覆盖当前筛选条件。

## 测试与验证

### 后端

- 默认按 `token_used DESC` 返回。
- `token_used`、`quota`、`count` 的升序和降序均正确。
- 同一用户跨模型、跨小时的数据正确累计。
- 用户改名后的历史记录按 `user_id` 合并，并显示当前用户名和显示名称。
- 所选范围内没有使用量的启用用户以三个 0 值进入排行。
- 历史上使用过但当前范围没有使用量的启用用户仍以 0 进入排行。
- 普通用户、管理员和 Root 在启用时都进入排行。
- 禁用用户和软删除用户不进入排行，即使存在范围内历史用量。
- 20、50、100 分页、总数和跨页排名基础数据正确。
- 主排序值相同时按 `user_id ASC` 稳定排序。
- 没有启用且未软删除用户时正常返回空数据；只有零用量用户时返回 0 值排行。
- 时间边界、反向时间、一年以上跨度及非法分页和排序参数被拒绝。
- 普通用户不能访问接口。
- 查询不使用数据库专属函数或操作符，满足 SQLite、MySQL 和 PostgreSQL 兼容要求。
- 聚合结果使用 `int64`，大数值不会按 `int32` 截断。

### 前端

- 初次加载使用近 7 天、第一页、每页 20 条和 Token 降序。
- 点击三个可排序表头会更新远程排序并回到第一页。
- 修改时间范围和分页大小会回到第一页。
- 排名按页码连续计算。
- 计费额度通过 `renderQuota()` 跟随系统展示设置。
- 加载、空结果、失败和请求竞态状态正确。
- 管理员可见入口并可访问页面，普通用户无法通过路由进入。
- 数据看板统计关闭时显示明确提示且不请求排行接口。

### 验证命令

实施阶段至少运行相关 Go 测试、前端格式或检查、i18n lint 和前端构建。前端命令使用 Bun。
