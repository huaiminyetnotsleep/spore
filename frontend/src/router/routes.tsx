/**
 * SPA 路由表统一维护：页面路径、导航配置与 404 都注册在本模块，
 * App.tsx 只负责 Provider 与布局组装。basename /admin 由 App.tsx 的
 * BrowserRouter 提供；保持既有路径与行为不变（含 404 兜底）。
 */
import {
  ApiOutlined,
  AuditOutlined,
  CheckCircleOutlined,
  BarChartOutlined,
  CloudDownloadOutlined,
  CloudServerOutlined,
  ControlOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  ExceptionOutlined,
  GithubOutlined,
  LineChartOutlined,
  LinkOutlined,
  SettingOutlined,
  TeamOutlined,
  UserOutlined,
  UsergroupAddOutlined,
} from "@ant-design/icons";
import { Result, Spin } from "antd";
import type { ReactNode } from "react";
import { Suspense } from "react";
import { matchPath, Route, Routes } from "react-router-dom";

import { AuditPage } from "../features/audit/AuditPage";
import { BackupPage } from "../features/backup/BackupPage";
import { BindingsPage } from "../features/bindings/BindingsPage";
import { CloudDrivePage } from "../features/cloud-drive/CloudDrivePage";
import { ChannelSettingsPage } from "../features/channel-settings/ChannelSettingsPage";
import { JoinApprovalsPage } from "../features/channel-join/JoinApprovalsPage";
import { JoinedChannelsPage } from "../features/channel-join/JoinedChannelsPage";
import { JoinSettingsPage } from "../features/channel-join/JoinSettingsPage";
import { ChannelDetailPage } from "../features/channels/ChannelDetailPage";
import { ChannelsListPage } from "../features/channels/ChannelsListPage";
import { EventsPage } from "../features/events/EventsPage";
import { MTProtoPage } from "../features/mtproto/MTProtoPage";
import { OAuthSettingsPage } from "../features/oauth/OAuthSettingsPage";
import { OverviewPage } from "../features/overview/OverviewPage";
import { RequestDetailPage } from "../features/requests/RequestDetailPage";
import { RequestsListPage } from "../features/requests/RequestsListPage";
import { SettingsPage } from "../features/settings/SettingsPage";
import { StatsPage } from "../features/stats/StatsPage";
import { SystemConfigPage } from "../features/system/SystemConfigPage";
import { ApplicationsPage } from "../features/users/ApplicationsPage";
import { UserDetailPage } from "../features/users/UserDetailPage";
import { UsersListPage } from "../features/users/UsersListPage";

export type NavigationRoute = {
  key: string;
  path: string;
  label: string;
  title: string;
  groupKey: string;
  icon: ReactNode;
  menuVisible: boolean;
  parentKey?: string;
};

/**
 * 页面元数据与实际路由同源维护：菜单、标题、面包屑和详情父级均从这里读取。
 * 详情路由只用于上下文定位，不会单独出现在侧边菜单中。
 */
export const routeMeta = [
  {
    key: "overview",
    path: "/",
    label: "总览",
    title: "总览",
    groupKey: "workspace",
    icon: <DashboardOutlined />,
    menuVisible: true,
  },
  {
    key: "stats",
    path: "/stats",
    label: "业务统计",
    title: "业务统计",
    groupKey: "workspace",
    icon: <LineChartOutlined />,
    menuVisible: true,
  },
  {
    key: "applications",
    path: "/applications",
    label: "申请审批",
    title: "申请审批",
    groupKey: "user-operations",
    icon: <ExceptionOutlined />,
    menuVisible: true,
  },
  {
    key: "users",
    path: "/users",
    label: "用户管理",
    title: "用户管理",
    groupKey: "user-operations",
    icon: <TeamOutlined />,
    menuVisible: true,
  },
  {
    key: "user-detail",
    path: "/users/:id",
    label: "用户详情",
    title: "用户详情",
    groupKey: "user-operations",
    icon: <UserOutlined />,
    menuVisible: false,
    parentKey: "users",
  },
  {
    key: "requests",
    path: "/requests",
    label: "请求记录",
    title: "请求记录",
    groupKey: "request-channels",
    icon: <LinkOutlined />,
    menuVisible: true,
  },
  {
    key: "request-detail",
    path: "/requests/:id",
    label: "请求详情",
    title: "请求详情",
    groupKey: "request-channels",
    icon: <LinkOutlined />,
    menuVisible: false,
    parentKey: "requests",
  },
  {
    key: "channels",
    path: "/channels",
    label: "频道统计",
    title: "频道统计",
    groupKey: "request-channels",
    icon: <BarChartOutlined />,
    menuVisible: true,
  },
  {
    key: "channel-bindings",
    path: "/channel-bindings",
    label: "频道绑定",
    title: "频道绑定",
    groupKey: "request-channels",
    icon: <ApiOutlined />,
    menuVisible: true,
  },
  {
    key: "channel-settings",
    path: "/channel-settings",
    label: "频道设置",
    title: "频道设置",
    groupKey: "request-channels",
    icon: <SettingOutlined />,
    menuVisible: true,
  },
  {
    key: "cloud-drive",
    path: "/cloud-drive",
    label: "云盘下载",
    title: "云盘下载",
    groupKey: "request-channels",
    icon: <CloudDownloadOutlined />,
    menuVisible: true,
  },
  {
    key: "invite-approvals",
    path: "/invite-approvals",
    label: "加入审批",
    title: "加入审批",
    groupKey: "channel-invited",
    icon: <CheckCircleOutlined />,
    menuVisible: true,
  },
  {
    key: "joined-channels",
    path: "/joined-channels",
    label: "已加入频道",
    title: "已加入频道",
    groupKey: "channel-invited",
    icon: <UsergroupAddOutlined />,
    menuVisible: true,
  },
  {
    key: "join-settings",
    path: "/join-settings",
    label: "受邀设置",
    title: "受邀设置",
    groupKey: "channel-invited",
    icon: <SettingOutlined />,
    menuVisible: true,
  },
  {
    key: "channel-detail",
    path: "/channels/:key",
    label: "频道详情",
    title: "频道详情",
    groupKey: "request-channels",
    icon: <BarChartOutlined />,
    menuVisible: false,
    parentKey: "channels",
  },
  {
    key: "events",
    path: "/events",
    label: "事件中心",
    title: "事件中心",
    groupKey: "events-audit",
    icon: <ExceptionOutlined />,
    menuVisible: true,
  },
  {
    key: "audit",
    path: "/audit",
    label: "审计日志",
    title: "审计日志",
    groupKey: "events-audit",
    icon: <AuditOutlined />,
    menuVisible: true,
  },
  {
    key: "settings",
    path: "/settings",
    label: "运行设置",
    title: "运行设置",
    groupKey: "system-operations",
    icon: <SettingOutlined />,
    menuVisible: true,
  },
  {
    key: "system-config",
    path: "/settings/system",
    label: "系统设置",
    title: "系统设置",
    groupKey: "system-operations",
    icon: <ControlOutlined />,
    menuVisible: true,
  },
  {
    key: "mtproto",
    path: "/mtproto",
    label: "Telegram 连接",
    title: "Telegram 连接",
    groupKey: "system-operations",
    icon: <CloudServerOutlined />,
    menuVisible: true,
  },
  {
    key: "oauth",
    path: "/settings/oauth",
    label: "GitHub 登录",
    title: "GitHub 登录",
    groupKey: "system-operations",
    icon: <GithubOutlined />,
    menuVisible: true,
  },
  {
    key: "backup",
    path: "/backup",
    label: "数据备份",
    title: "数据备份",
    groupKey: "system-operations",
    icon: <DatabaseOutlined />,
    menuVisible: true,
  },
] as const satisfies readonly NavigationRoute[];

export type RouteKey = (typeof routeMeta)[number]["key"];
export type RoutePath = (typeof routeMeta)[number]["path"];

export type NavigationGroup = {
  key: string;
  label: string;
  routeKeys: readonly RouteKey[];
};

/** 目标信息架构的五个业务域分组；分组本身不绑定页面路径。 */
export const navigationGroups = [
  { key: "workspace", label: "工作台", routeKeys: ["overview", "stats"] },
  {
    key: "request-channels",
    label: "请求与频道",
    routeKeys: ["requests", "channels", "channel-bindings", "channel-settings", "cloud-drive"],
  },
  {
    key: "channel-invited",
    label: "BotUser受邀频道",
    routeKeys: ["invite-approvals", "joined-channels", "join-settings"],
  },
  {
    key: "user-operations",
    label: "用户运营",
    routeKeys: ["applications", "users"],
  },
  {
    key: "events-audit",
    label: "事件与审计",
    routeKeys: ["events", "audit"],
  },
  {
    key: "system-operations",
    label: "系统运维",
    routeKeys: ["settings", "system-config", "mtproto", "oauth", "backup"],
  },
] as const satisfies readonly NavigationGroup[];

const routeByKey = new Map<string, NavigationRoute>(
  routeMeta.map((route) => [route.key, route]),
);

const groupByKey = new Map<string, NavigationGroup>(
  navigationGroups.map((group) => [group.key, group]),
);

/** 兼容既有调用方：仅返回菜单叶子路由，不包含详情页。 */
export const navRoutes = routeMeta.filter((route) => route.menuVisible);

function matchesRoutePath(pathname: string, path: string): boolean {
  // end=true 防止 /users/123/extra 等实际 404 路径误继承详情页上下文。
  return matchPath({ path, end: true }, pathname) !== null;
}

/** 根据当前 pathname 找到菜单选中项；详情页返回其父列表路由。 */
export function getActiveNavRoute(pathname: string): NavigationRoute | undefined {
  const current: NavigationRoute | undefined = routeMeta.find((route) =>
    matchesRoutePath(pathname, route.path),
  );
  if (!current) {
    return undefined;
  }
  return current.menuVisible || !current.parentKey
    ? current
    : routeByKey.get(current.parentKey);
}

/** 当前页面上下文，用于布局标题和面包屑。 */
export function getNavigationContext(pathname: string) {
  const current: NavigationRoute | undefined = routeMeta.find((route) =>
    matchesRoutePath(pathname, route.path),
  );
  if (!current) {
    return undefined;
  }
  const parent = current.parentKey ? routeByKey.get(current.parentKey) : undefined;
  const group = groupByKey.get(current.groupKey);
  return { current, parent, group };
}

/** 侧边导航高亮规则：详情页继承父列表，OAuth 不会同时命中运行设置。 */
export function isActivePath(pathname: string, path: RoutePath): boolean {
  return getActiveNavRoute(pathname)?.path === path;
}

function NotFoundPage() {
  return <Result status="404" title="页面不存在" subTitle="请从左侧导航选择一个管理端页面。" />;
}

/** SPA 路由树：全部管理页面均已挂载真实页面，另含 404 兜底。 */
export function AppRoutes() {
  return (
    <Suspense fallback={<Spin className="page-loading" tip="页面加载中…" />}>
      <Routes>
        {/* 只读页面：总览、业务统计、申请、用户、请求、频道、事件 */}
        <Route path="/" element={<OverviewPage />} />
        <Route path="/stats" element={<StatsPage />} />
        <Route path="/applications" element={<ApplicationsPage />} />
        <Route path="/users" element={<UsersListPage />} />
        <Route path="/users/:id" element={<UserDetailPage />} />
        <Route path="/requests" element={<RequestsListPage />} />
        <Route path="/requests/:id" element={<RequestDetailPage />} />
        <Route path="/channels" element={<ChannelsListPage />} />
        <Route path="/channels/:key" element={<ChannelDetailPage />} />
        <Route path="/channel-bindings" element={<BindingsPage />} />
        <Route path="/channel-settings" element={<ChannelSettingsPage />} />
        <Route path="/cloud-drive" element={<CloudDrivePage />} />
        <Route path="/invite-approvals" element={<JoinApprovalsPage />} />
        <Route path="/joined-channels" element={<JoinedChannelsPage />} />
        <Route path="/join-settings" element={<JoinSettingsPage />} />
        <Route path="/events" element={<EventsPage />} />
        {/* 系统运维页面：写操作仍统一走 /api/v1 */}
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="/settings/system" element={<SystemConfigPage />} />
        <Route path="/settings/oauth" element={<OAuthSettingsPage />} />
        <Route path="/backup" element={<BackupPage />} />
        <Route path="/mtproto" element={<MTProtoPage />} />
        <Route path="/audit" element={<AuditPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Routes>
    </Suspense>
  );
}
