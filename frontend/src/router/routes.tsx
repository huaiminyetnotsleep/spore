/**
 * SPA 路由表统一维护：页面路径、导航配置与 404 都注册在本模块，
 * App.tsx 只负责 Provider 与布局组装。basename /admin 由 App.tsx 的
 * BrowserRouter 提供；保持既有路径与行为不变（含 404 兜底）。
 *
 * 页面组件全部经 React.lazy 路由级分包：每个页面（及其独占的 antd/rc-*
 * 组件）拆成独立 chunk，按导航按需加载；AppRoutes 内的 Suspense 提供
 * 统一的加载兜底。路由元数据（routeMeta）本身保持同步加载，菜单渲染
 * 不受影响。
 */
import {
  ApiOutlined,
  EyeOutlined,
  AuditOutlined,
  BugOutlined,
  CheckCircleOutlined,
  BarChartOutlined,
  BellOutlined,
  CloudDownloadOutlined,
  CloudServerOutlined,
  ControlOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  ExceptionOutlined,
  HistoryOutlined,
  GithubOutlined,
  RobotOutlined,
  LineChartOutlined,
  LinkOutlined,
  SettingOutlined,
  TeamOutlined,
  UserOutlined,
  UsergroupAddOutlined,
} from "@ant-design/icons";
import { Result, Spin, Button } from "antd";
import type { ReactNode } from "react";
import { lazy, Suspense } from "react";
import { matchPath, Route, Routes, useNavigate } from "react-router-dom";

const OverviewPage = lazy(() =>
  import("../features/overview/OverviewPage").then((m) => ({ default: m.OverviewPage })),
);
const StatsPage = lazy(() =>
  import("../features/stats/StatsPage").then((m) => ({ default: m.StatsPage })),
);
const ApplicationsPage = lazy(() =>
  import("../features/users/ApplicationsPage").then((m) => ({ default: m.ApplicationsPage })),
);
const UsersListPage = lazy(() =>
  import("../features/users/UsersListPage").then((m) => ({ default: m.UsersListPage })),
);
const UserDetailPage = lazy(() =>
  import("../features/users/UserDetailPage").then((m) => ({ default: m.UserDetailPage })),
);
const RequestsListPage = lazy(() =>
  import("../features/requests/RequestsListPage").then((m) => ({ default: m.RequestsListPage })),
);
const RequestDetailPage = lazy(() =>
  import("../features/requests/RequestDetailPage").then((m) => ({ default: m.RequestDetailPage })),
);
const ChannelsListPage = lazy(() =>
  import("../features/channels/ChannelsListPage").then((m) => ({ default: m.ChannelsListPage })),
);
const ChannelDetailPage = lazy(() =>
  import("../features/channels/ChannelDetailPage").then((m) => ({ default: m.ChannelDetailPage })),
);
const BindingsPage = lazy(() =>
  import("../features/bindings/BindingsPage").then((m) => ({ default: m.BindingsPage })),
);
const ChannelSettingsPage = lazy(() =>
  import("../features/channel-settings/ChannelSettingsPage").then((m) => ({
    default: m.ChannelSettingsPage,
  })),
);
const WatchSourcesPage = lazy(() =>
  import("../features/watch-sources/WatchSourcesPage").then((m) => ({
    default: m.WatchSourcesPage,
  })),
);
const WatchSettingsPage = lazy(() =>
  import("../features/watch-sources/WatchSettingsPage").then((m) => ({
    default: m.WatchSettingsPage,
  })),
);
const WatchEventsPage = lazy(() =>
  import("../features/watch-sources/WatchEventsPage").then((m) => ({
    default: m.WatchEventsPage,
  })),
);
const CloudDrivePage = lazy(() =>
  import("../features/cloud-drive/CloudDrivePage").then((m) => ({ default: m.CloudDrivePage })),
);
const JoinApprovalsPage = lazy(() =>
  import("../features/channel-join/JoinApprovalsPage").then((m) => ({
    default: m.JoinApprovalsPage,
  })),
);
const JoinedChannelsPage = lazy(() =>
  import("../features/channel-join/JoinedChannelsPage").then((m) => ({
    default: m.JoinedChannelsPage,
  })),
);
const JoinSettingsPage = lazy(() =>
  import("../features/channel-join/JoinSettingsPage").then((m) => ({
    default: m.JoinSettingsPage,
  })),
);
const EventsPage = lazy(() =>
  import("../features/events/EventsPage").then((m) => ({ default: m.EventsPage })),
);
const ErrorLogsPage = lazy(() =>
  import("../features/error-logs/ErrorLogsPage").then((m) => ({ default: m.ErrorLogsPage })),
);
const SettingsPage = lazy(() =>
  import("../features/settings/SettingsPage").then((m) => ({ default: m.SettingsPage })),
);
const SystemConfigPage = lazy(() =>
  import("../features/system/SystemConfigPage").then((m) => ({ default: m.SystemConfigPage })),
);
const NotificationSettingsPage = lazy(() =>
  import("../features/notification/NotificationSettingsPage").then((m) => ({
    default: m.NotificationSettingsPage,
  })),
);
const OAuthSettingsPage = lazy(() =>
  import("../features/oauth/OAuthSettingsPage").then((m) => ({ default: m.OAuthSettingsPage })),
);
const BackupPage = lazy(() =>
  import("../features/backup/BackupPage").then((m) => ({ default: m.BackupPage })),
);
const BotsPage = lazy(() =>
  import("../features/bots/BotsPage").then((m) => ({ default: m.BotsPage })),
);
const MTProtoPage = lazy(() =>
  import("../features/mtproto/MTProtoPage").then((m) => ({ default: m.MTProtoPage })),
);
const AuditPage = lazy(() =>
  import("../features/audit/AuditPage").then((m) => ({ default: m.AuditPage })),
);

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
    key: "watch-events",
    path: "/watch-events",
    label: "监听记录",
    title: "监听记录",
    groupKey: "watch-group",
    icon: <HistoryOutlined />,
    menuVisible: true,
  },
  {
    key: "watch-sources",
    path: "/watch-sources",
    label: "监听源管理",
    title: "监听源管理",
    groupKey: "watch-group",
    icon: <EyeOutlined />,
    menuVisible: true,
  },
  {
    key: "watch-settings",
    path: "/watch-settings",
    label: "监听源配置",
    title: "监听源配置",
    groupKey: "watch-group",
    icon: <SettingOutlined />,
    menuVisible: true,
  },
  {
    key: "cloud-drive",
    path: "/cloud-drive",
    label: "云盘下载",
    title: "云盘下载",
    groupKey: "system-operations",
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
    key: "error-logs",
    path: "/error-logs",
    label: "错误日志",
    title: "错误日志",
    groupKey: "events-audit",
    icon: <BugOutlined />,
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
    key: "notification",
    path: "/settings/notification",
    label: "通知设置",
    title: "通知设置",
    groupKey: "system-operations",
    icon: <BellOutlined />,
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
  {
    key: "bots",
    path: "/bots",
    label: "机器人池管理",
    title: "机器人池管理",
    groupKey: "bot-management",
    icon: <RobotOutlined />,
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
    routeKeys: ["requests", "channels", "channel-bindings", "channel-settings"],
  },
  {
    key: "watch-group",
    label: "监听源",
    routeKeys: ["watch-events", "watch-sources", "watch-settings"],
  },
  {
    key: "channel-invited",
    label: "MTProto受邀管理",
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
    routeKeys: ["events", "error-logs", "audit"],
  },
  {
    key: "bot-management",
    label: "机器人池",
    routeKeys: ["bots"],
  },
  {
    key: "system-operations",
    label: "系统运维",
    routeKeys: [
      "settings",
      "system-config",
      "notification",
      "mtproto",
      "cloud-drive",
      "oauth",
      "backup",
    ],
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

/** 通配 404：与详情不存在（DetailNotFound）一致的主返回动作与页面宽度。 */
function NotFoundPage() {
  const navigate = useNavigate();
  return (
    <Result
      status="404"
      title="页面不存在"
      subTitle="请从左侧导航选择一个管理端页面。"
      extra={
        <Button type="primary" onClick={() => navigate("/")}>
          返回总览
        </Button>
      }
    />
  );
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
        <Route path="/watch-sources" element={<WatchSourcesPage />} />
        <Route path="/watch-settings" element={<WatchSettingsPage />} />
        <Route path="/watch-events" element={<WatchEventsPage />} />
        <Route path="/cloud-drive" element={<CloudDrivePage />} />
        <Route path="/invite-approvals" element={<JoinApprovalsPage />} />
        <Route path="/joined-channels" element={<JoinedChannelsPage />} />
        <Route path="/join-settings" element={<JoinSettingsPage />} />
        <Route path="/events" element={<EventsPage />} />
        <Route path="/error-logs" element={<ErrorLogsPage />} />
        {/* 系统运维页面：写操作仍统一走 /api/v1 */}
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="/settings/system" element={<SystemConfigPage />} />
        <Route path="/settings/notification" element={<NotificationSettingsPage />} />
        <Route path="/settings/oauth" element={<OAuthSettingsPage />} />
        <Route path="/backup" element={<BackupPage />} />
        <Route path="/bots" element={<BotsPage />} />
        <Route path="/mtproto" element={<MTProtoPage />} />
        <Route path="/audit" element={<AuditPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Routes>
    </Suspense>
  );
}
