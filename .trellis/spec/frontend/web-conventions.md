# Spore 前端（SPA 管理端）开发规范

本层适用于 `frontend/`（React 19 + TypeScript + Vite + Ant Design 5 + React Router 7 + TanStack Query 5 + Vitest + Playwright）。2026-09 起管理端全部页面（含登录页）均为 SPA；Go 侧唯一 HTML 输出是应用壳，服务端契约见 [backend/web-serving.md](../backend/web-serving.md)。

## 硬性约定

1. **CSP 禁内联**：禁止 `style={{...}}`（生产 CSP `style-src-attr 'unsafe-inline'` 仅作为 AntD 内部兼容兜底，应用代码不得新增内联样式）。固定布局样式写进 `styles.css` 的 class；动态样式需在代码注释说明理由。`grep "style={{" frontend/src` 必须零匹配。
2. **API 只走 `/api/v1`**：请求统一经 `src/api/client.ts`（错误信封 `{error:{code,message}}` → `ApiError`）；列表信封 `{"items","page","page_size","total","total_pages"}` 对应 `ListEnvelope<T>`；时间一律 Unix 毫秒。**不得绕过 client 直接裸写 fetch 业务接口**（例外：文件下载用 `<Button href>` 指向 CSV 端点）。
3. **两套 CSRF**：登录用 `GET /api/v1/login/csrf` 的 token；会话写操作用 `GET /api/v1/session` 引导的 token——两者独立，不得混用（见 `api/session.ts`）。
4. **服务端受控文案**：UI 错误提示只渲染 `ApiError.message`（后端受控信封）；查询参数驱动的状态（如 OAuth 结果 `?oauth=bound|error`）必须做枚举白名单，展示后 `replaceState` 清参。
5. **路由与布局**：路由表统一在 `src/router/routes.tsx`；`/login`（basename `/admin` 下）是独立布局的公开登录页，不挂侧边栏与会话引导；其余页面经 `AppLayout` 的会话引导（401 整页跳 `/admin/login`，防死循环的关键：登录页自身不发起引导查询）。

## 统一 UI 基础（2026-09-16 起）

2026-09-16 unify-frontend-ui 任务后，管理端页面必须复用以下共享基础，不得再各页自建同义实现：

1. **视觉值单一来源**：`src/theme.ts` 是 ConfigProvider theme、图表 `chartPalette` 与状态 `statusPalette` 的唯一定义处；`styles.css` 顶部 `:root` 语义变量承载边框/圆角/阴影/状态浅底。页面不得新增 feature 级颜色/圆角/阴影常量。
2. **页面骨架**：`PageScaffold`（唯一 H1，可访问名与路由标签一致）+ `PageSection` + `ResponsiveActionBar`/`PageActions`；`PageCard`/`SectionCard` 仅作为兼容适配层存在（存量：StatsCharts、SnapshotSection），新页面禁止直接使用。
3. **Query 状态**：`QueryStates.tsx` 提供 Page/Section/Table/Inline 四级边界与 `EmptyState`；初始加载（`isPending && !data`）与后台刷新必须区分，后台刷新保留已有内容（`aria-busy`），错误只影响对应边界。
4. **异步设置 Gate**：设置类页面在首次数据到达前不得渲染可提交控件或清除/解绑入口（先例：ChannelSettings/JoinSettings/OAuthSettings/MTProto）。
5. **确认契约**：`useConfirmAction` 接收 `{ intent, title, content, okText?, cancelText?, action }`，`action` 返回 Promise 让弹层真实 pending、防重复提交；`danger` 仅限删除/清除/退出/解绑/整库替换等破坏性操作，暂停/重启/取消/拒绝/状态变更用 `warning`，重试/转存/导出用 `default`；旧 `confirm(content, onOk)` 仅作兼容保留，新代码禁用。批量与单条同语义动作确认强度必须一致；批量提交必须快照 selection 并在 pending 期间冻结勾选。
6. **反馈契约**：`useAdminAction` 成功反馈支持 `string | { type: "success"|"warning"|"info", text }`；部分成功（如审批成功但通知失败）必须用 warning，不得显示全成功。行级操作用 `busy*Id` 目标 key，只让目标行进入 loading。
7. **高频组件**：`FilterBar`（submit=筛选/重置，instant 不渲染应用按钮；窄屏字段全宽）、`DataTable`（默认 middle 密度、`scroll.x="max-content"`、详情附表 `density="compact"`；调用方可覆盖但不得关掉表内滚动）、`FormModal`（默认关闭重置；`preserveOnClose` 显式保留草稿；pending 防重复；仅在 action 返回成功标记后关闭）、`FormActions`（设置页保存右对齐）、`SettingItem`（标签+描述+控件+状态）、`StatusTag`/`statusTone`（tone→AntD 色映射；领域状态→tone 映射留在领域模块）。

## 导航与高风险操作约定

1. **路由元数据单一来源**：菜单标签、页面标题、分组、图标、详情父级和菜单可见性应从路由元数据派生；实际路由仍只在 `src/router/routes.tsx` 登记。详情页设置 `menuVisible: false` 并声明 `parentKey`，不得为详情创建重复顶层菜单。
2. **详情匹配必须完整**：计算菜单选中态和面包屑时，对参数路由使用 React Router `matchPath({ path, end: true }, pathname)` 或等价的完整路径匹配；不能用裸 `startsWith`，否则 `/users/123/extra` 等 404 会错误继承详情上下文。
3. **受保护操作入口**：退出登录、受控重启等账户/服务级操作可集中放在受保护布局 Header 的 Avatar Dropdown 中；破坏性操作使用菜单项 `danger: true`，并复用已有 mutation、确认文案、loading 和受控错误反馈。页面正文保留状态说明，但不要复制同一触发动作。
4. **导航测试点**：至少覆盖分组/叶子数量、默认展开策略、列表详情父级选中、特殊子路由独立选中、Avatar 菜单高风险操作触发和非认证页面不显示受保护操作。

## 构建与 embed 同步

```bash
cd frontend && npm run build   # tsc --noEmit + vite build
```

构建产物需同步到 `internal/web/frontend-dist/`（Go embed 才会生效）。注意：`make frontend-build` 在依赖 stamp 过期时会触发 `npm ci`，本机 2 分钟超时内跑不完——日常用 `npm run build` + Makefile 中的 find+cp 同步命令。

## 测试门禁（完成修改后全部执行）

```bash
npm run lint        # --max-warnings=0
npm run typecheck
npm test            # Vitest + Testing Library；jsdom/antd stderr 告警为既有噪音
npm run build
npm run test:e2e    # 默认替身模式（Vite + 假 API）
# 隔离 Compose/真机模式：
PLAYWRIGHT_BASE_URL=http://127.0.0.1:<port> PLAYWRIGHT_ACCESS_KEY='<隔离密钥>' npm run test:e2e
```

## 测试要求

- 新页面必须有单测：渲染、关键交互、错误态（参考 `AuditPage.test.tsx`、`LoginPage.test.tsx`）。
- e2e（`e2e/app-shell.spec.ts`）需覆盖：登录全流程（断言 CSRF 头与请求体）、SPA 导航/深链刷新、session 401/CSRF 403 错误态、未知 API 404 不被 SPA 截获、CSV 下载与旧页面入口 404、CSP nonce 无 `securitypolicyviolation`。
- 全路由响应式 smoke（`e2e/responsive-smoke.spec.ts`）：400px 逐路由断言唯一 H1、页面容器 `scrollWidth <= clientWidth`、操作按钮在 viewport 内可点击、宽表仅在 `.ant-table-content` 内滚动；1440/1024/768 覆盖代表性列表/详情/图表/设置页。结构性改动（页面骨架、表格、筛选、Modal）必须保持该 spec 全绿；新增路由需同步加入 400px 轮询列表，详情路由使用 fake API 固定样例 id/key。
- 涉及会话/登录的测试注意：每次登录都会在后端写一条 `auth.login` 审计。
- **Form 内 antd Select 的交互测试**：选项用 `options` 属性声明并加 `virtual={false}`（全量渲染，见 `RequestsListPage.tsx` 补存目的地先例）；测试中 `fireEvent.mouseDown(selector)` 打开后 `findByRole("option", { name })` 点击。children 形式（`<Select.Option>`）的选项在 jsdom 中点击不更新表单值（09-10 per-user-cloud-download 调试结论），两字按钮断言记得插空格（`"确 认"`），三字及以上不插。

## 场景：请求记录当前页批量取消

### 1. Scope / Trigger

- Trigger：请求记录页增加单条或批量取消请求。
- Scope：只覆盖当前页明确勾选的 ID，不把筛选结果或其他页面记录隐式扩展进操作。

### 2. Signatures

```typescript
cancelRequest(id: number): Promise<WriteOK>
cancelRequests(ids: number[]): Promise<CancelManyResult>
```

```text
POST /api/v1/requests/{id}/cancel
POST /api/v1/requests/cancel {"ids":[12,13]}
```

### 3. Contracts

- 取消按钮仅对 `queued`/`processing` 显示；终态记录不显示取消，`cancelled` 显示“已取消”。
- Table selection 只提交当前页明确选中的正整数 ID；切换分页或筛选必须清空 selection。
- 批量响应是逐条结果，UI 必须展示成功、冲突、不存在或失败，不得把部分成功显示为全成功。
- 写请求经 `postJSON`、会话 CSRF 和服务端受控 `ApiError.message`；成功后失效 requests/channels/overview 等查询。

### 4. Validation & Error Matrix

| 条件 | 行为 |
| --- | --- |
| 未选择记录 | 不显示批量取消动作 |
| 选中包含终态记录 | checkbox 禁用；服务端仍复核并返回 conflict |
| 取消确认被拒绝 | 不调用 API，保留选择 |
| 批量部分成功 | 显示逐条结果并清空本页选择，刷新查询 |
| API 409/5xx | 只展示受控 message，不显示底层错误 |

### 5. Good / Base / Bad Cases

- Good：确认弹窗显示数量，提交冻结按钮，响应后刷新并显示逐条结果。
- Base：单条取消复用 `useAdminAction`、确认和 loading。
- Bad：按筛选条件猜测全量 ID、跨页保留旧选择、或直接渲染 fetch/服务器原始错误。

### 6. Tests Required

- 列表：活动状态 checkbox/按钮、当前页批量提交、未选行不受影响、切页/筛选清空选择。
- 详情：活动状态取消入口和成功刷新；已取消状态无取消入口。
- API：mutation path/body、批量逐条结果和受控错误。
- 默认门禁：`npm run lint`、`npm run typecheck`、`npm test`、`npm run build`。

### 7. Wrong vs Correct

#### Wrong

```typescript
// 把当前筛选条件当成全量取消范围，可能误取消未展示记录。
cancelRequests(allMatchingRequestIds);
```

#### Correct

```typescript
// 只提交 Table 当前页由管理员明确勾选的 ID。
cancelRequests(selectedIDs);
```

## 场景：统计图表跨层契约（/stats 与横向 Bar）

### 1. Scope / Trigger

- Trigger：修改 `GET /api/v1/stats` 的趋势、排行、分布字段，或修改 `frontend/src/features/stats/`、`frontend/src/features/shared/RankBarChart.tsx` 的图表配置。
- Scope：覆盖 SQLite 聚合、Web DTO、React 类型和图表坐标/Tooltip；图表区域只展示图表，不在图表下方重复渲染表格。

### 2. 页面职责边界（2026-09-08 重构后）

- 总览 `/`（`features/overview/OverviewPage.tsx`）：只回答”系统现在怎么样”——状态一览彩色状态卡（MTProto/数据库/队列水位/待办，`.status-tile--ok/--warn/--error`）+ 紧凑服务信息 + 独立系统资源监控（进程 RSS、临时目录、上传/下载速率）。**不放业务统计图表**。
- 业务统计 `/stats`（`features/stats/StatsPage.tsx`）：业务数字——时间范围工具栏（快捷范围 Segmented：今天/近 7 天/近 30 天/全量，立即生效；自定义范围走单个 `DatePicker.RangePicker` 选完即生效、无应用按钮，生效范围回显在 extra）→ 核心指标（请求总数在卡片标题行 extra，成功/失败/未完成经 `MetricItem.stats` 合并一张卡）→ 趋势/排行/分布三个 PageCard 区块 → 全时段快照（`SnapshotSection`，`[“overview”]` 查询缓存复用，独立 loading/error，全时段口径不受时间筛选影响）。
- 数据源：`/api/v1/stats` 纯业务时间范围口径；`/api/v1/overview` 纯实时快照；`/api/v1/system-metrics` 提供系统监控样本。不得把业务统计塞进 overview，也不得把系统监控混入 stats。

### 3. Signatures

```text
GET /api/v1/stats?since=YYYY-MM-DD&until=YYYY-MM-DD[&all=1]
requests.trend[]: {day, total, succeeded, failed, error_rate: number|null}
requests.top_channels[]: {key, total, succeeded, failed, success_rate, last_requested_at}
requests.top_users[]: {id, username, display_name, total, succeeded, failed, success_rate, last_requested_at}
requests.media_dist[] / requests.error_dist[]: {key, count, ratio?}
requests.dc_dist[]: {key, count}   // key 为 DC ID 十进制串，空串 = 未记录；跨多 DC 的请求在每个 DC 各计一次
requests.dc_trend[]: {day, dist: [{key, count}]}  // 范围内空日期补齐（dist 为空数组）；全量模式只含有数据日期
```

```typescript
// 横向 Bar 契约的唯一定义处是 shared/RankBarChart.tsx：
{ xField: “label”, yField: “count” }
// 组件内部转置：视觉横轴为数量，纵轴为分类 label；Tooltip 数值读取 channel: “y”；
// 条目点击经 barNavigation(navigate, navigateTo) 跳转，navigateTo 返回 null 不跳。
```

### 4. 公共组件（不得再各页复制配置）

- `features/shared/RankBarChart.tsx`：所有横向排行/分布 Bar（频道/用户/媒体/错误原因/加入来源）必须复用它，不得手写 `BarConfig` 副本。
- `features/shared/ChartPanel.tsx`：图表块外壳（标题/副描述/空态）；卡片底板由页面层 PageSection/PageCard 承担。
- `features/shared/StatGrid.tsx`：CSS Grid + `Statistic` 指标网格；样式类为 `.stat-grid`/`.chart-grid`/`.chart-panel__body`（旧的 `.overview-chart-*` 已删除）。
- 频道加入来源中文标签统一 `shared/format.ts` 的 `JOIN_SOURCE_LABELS`/`joinSourceLabel`，页面不得再本地定义。

### 5. Contracts

- 请求结果趋势：X 轴为运营时区日期，Y 轴为请求数；成功/失败是固定系列。
- 错误率：`failed / (succeeded + failed)`；无终态样本的日点为 `null`，不能绘制成 0。
- 频道 Top 5、用户 Top 10、错误原因 Top 5 +”其他”由服务端稳定排序；前端不接收原始请求行自行聚合。
- 快捷时间范围（今天/近 7 天/近 30 天/全量）：前三者显式计算 since/until 立即生效；全量以 `all=1` 查询、忽略时间范围，RangePicker 清空并禁用，回显“统计范围：全量”（`since_day/until_day` 为空串）。自定义范围选完立即查询；清空 RangePicker 恢复缺省近 7 天。生效范围回显以服务端 `since_day/until_day` 为准。
- 时间轴图（按日错误率折线、按日源媒体 DC 堆叠柱状）横轴必须覆盖完整筛选范围：有明确起止范围时后端补齐空日期。错误率空日在底部画空心点、折线以 mark `style.connect: true` 跨日连接（不剔除日期、不补 0；色彩通道接触 null 会使阈值色阶整条折线渲染失败，需用哨兵值字段）；DC 堆叠柱前端按序列全集为空日补 0。全量模式（无界范围）只显示有数据日期。
- 源媒体 DC 口径：按请求数计，一条请求的媒体跨多个 DC 时在每个 DC 各计一次；空数组/纯文本/旧记录计为“未记录”（空串 key）；DC 图不做切片跳转（请求列表尚无 DC 筛选参数）。
- 精确数量通过 Tooltip、必要的数据标签和图表交互提供；不要在图表区块下方追加重复 Table。

### 6. Validation & Error Matrix

| 条件 | 行为 |
| --- | --- |
| 日期范围内无请求 | KPI 为 0，分布为空，趋势保留范围日期；错误率无终态日期为 null |
| 用户展示名或用户名为空 | 图表 label 回退为用户 ID |
| 错误原因超过 Top N | 服务端合并为”其他”，不为”其他”生成单一错误码筛选 |
| 排行图数值与分类轴错位 | 检查 RankBarChart 的 `xField/yField`、轴标题和 Tooltip channel；不得仅通过改 CSS 掩盖 |
| API 辅助聚合失败 | 按服务端约定返回空列表并记录受控日志；前端显示可观察空态 |
| 快照区 overview 查询失败 | 只在「全时段快照」区块内展示错误与重试，不影响统计区（反之亦然） |

### 7. Good/Base/Bad Cases

- Good：排行/分布图全部走 `RankBarChart`，Tooltip 主数值 `channel: “y”`，浏览器截图确认横轴数量、纵轴分类。
- Base：趋势使用 `Column`（日期/请求数），错误率使用 `Line`（日期/错误率），排行使用 `RankBarChart`。
- Bad：在页面手写 `BarConfig` 副本、在统计页图表下复制统计 Table、或把全时段快照指标混进时间范围口径。

### 8. Tests Required

- API：断言趋势日期补齐、`error_rate: null`、成功/错误率分母、频道/用户 Top N、错误”其他”与比例。
- Frontend：断言排行 `label/count` 字段方向、Tooltip 数值字段、空态和无 Table；快捷范围点击立即带显式日期查询；快照失败隔离；图表依赖应可懒加载。
- Browser：检查实际排行图横轴为数量、纵轴为分类，Tooltip 能显示数值；构建产物同步到 `internal/web/frontend-dist/`。

### 9. Wrong vs Correct

#### Wrong

```typescript
// 绕过 RankBarChart 手写 Bar，且把 count 配到 xField：视觉分类跑到横轴。
<Bar xField=”count” yField=”label” />
```

#### Correct

```typescript
// 统一走 shared/RankBarChart（内部固定 xField label / yField count / barNavigation）。
<RankBarChart
  data={rows}
  color=”#1677ff”
  xAxisTitle=”频道”
  yAxisTitle=”请求数”
  tooltip={{ items: [{ channel: “y”, name: “请求总数” }] }}
  navigateTo={(d) => `/channels/${encodeURIComponent(d.key)}`}
/>
```
