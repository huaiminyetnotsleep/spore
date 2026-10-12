# Spore 前端（SPA 管理端）开发规范

本层适用于 `frontend/`（React SPA 管理端）。2026-09 起管理端全部页面均为 SPA，Go 侧不再渲染 HTML；服务端形态与登录安全契约见 [backend/web-serving.md](../backend/web-serving.md)。

## 规范索引

| 规范 | 内容 | 状态 |
| --- | --- | --- |
| [Web 约定](./web-conventions.md) | CSP 禁内联、API 信封、两套 CSRF、路由布局、构建同步与测试门禁 | 已填写 |

## 开发前检查清单

- [ ] 新页面路由登记在 `src/router/routes.tsx`，公开页（当前仅登录页）与受保护页的布局分流是否考虑。
- [ ] 页面是否使用 `PageScaffold`（唯一 H1）+ `PageSection` 骨架与共享组件（`DataTable`/`FilterBar`/`FormModal`/`QueryStates`/`StatusTag`/`SettingItem`）；视觉值是否引用 `theme.ts` token/palette 与语义 CSS 变量，不新增 feature 级颜色常量（详见 [web-conventions.md](./web-conventions.md) 统一 UI 基础）。
- [ ] 确认操作是否声明 `default/warning/danger` intent、`action` 返回 Promise；部分成功是否用 warning；行级操作是否目标级 pending；批量提交是否快照并冻结 selection。
- [ ] 异步设置页在首次数据到达前是否 gate 住可提交/清除控件。
- [ ] 样式是否全部走 `styles.css` class（禁 `style={{...}}`）。
- [ ] API 调用是否经 `src/api/client.ts` 并使用 `src/api/*` 的既有类型与信封。
- [ ] 错误文案是否只来自服务端受控信封；查询参数状态是否做了枚举白名单。
- [ ] 是否为新页面/交互补了单测与 e2e 断言点。
- [ ] 构建产物是否同步到 `internal/web/frontend-dist/`（embed 生效前提）。
- [ ] 对照[文档同步约束](../guides/docs-sync.md)：管理端新增/变更字段时，`docs/reference/api.md` 的接口字段表与用户侧文档是否需要同步。

## 质量检查

完成修改后至少运行（详见 [web-conventions.md](./web-conventions.md)）：

```bash
npm run lint && npm run typecheck && npm test && npm run build
```

`npm run test:e2e` 默认 fake API 模式运行 `app-shell`、`responsive-smoke`（全路由 400px/1440/1024/768 结构断言）与 `system-metrics-tooltip` 三个 spec；结构性 UI 改动必须保持全绿，新增路由需加入 smoke 的 400px 轮询列表。隔离 Compose/真机模式见 [web-conventions.md](./web-conventions.md)。

提交前按 [docs-sync.md](../guides/docs-sync.md) 做文档同步检查：页面字段、监控图表与 `docs/reference/api.md`、`docs/guide/` 的描述保持一致。
