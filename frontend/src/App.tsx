/**
 * 管理端 SPA 应用壳：只负责 Provider（React Query / antd / Router）与布局
 * 组装、会话引导（CSRF token 缓存）与登出；路由表统一在 src/router 维护。
 * 当前 /admin/login 是公开登录页：该路径渲染独立布局（无侧边栏、不发起
 * 会话引导），其余路径渲染管理端主布局并挂载会话引导查询。
 */
import { useQuery } from "@tanstack/react-query";
import {
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  ReloadOutlined,
  UserOutlined,
} from "@ant-design/icons";
import {
  App as AntApp,
  Avatar,
  Breadcrumb,
  Dropdown,
  Layout,
  Menu,
  Result,
  Typography,
} from "antd";
import type { MenuProps } from "antd";
import { Component, useEffect, useMemo, useState, type ReactNode } from "react";
import { BrowserRouter, Link, NavLink, useLocation, useNavigate } from "react-router-dom";

import { ApiError } from "./api/client";
import { logout } from "./api/mutations";
import { fetchSessionBootstrap } from "./api/session";
import { LoginPage } from "./features/login/LoginPage";
import { useRestartAction } from "./features/settings/restartAction";
import { useAdminAction } from "./features/shared/actions";
import { appName } from "./shared/appName";
import {
  AppRoutes,
  getActiveNavRoute,
  getNavigationContext,
  navigationGroups,
  routeMeta,
  type RouteKey,
} from "./router/routes";
import "./styles.css";

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

const routeByKey = new Map<RouteKey, (typeof routeMeta)[number]>(
  routeMeta.map((route) => [route.key, route]),
);

function Navigation() {
  const location = useLocation();
  const activeRoute = getActiveNavRoute(location.pathname);
  const activeKey = activeRoute?.key;
  // 默认只展开工作台，其他业务域由用户主动展开。
  const defaultOpenKeys = ["workspace"];

  const menuItems: MenuProps["items"] = useMemo(
    () =>
      navigationGroups.map((group) => ({
        key: group.key,
        label: group.label,
        children: group.routeKeys.map((routeKey) => {
          const route = routeByKey.get(routeKey);
          if (!route) {
            return null;
          }
          return {
            key: route.key,
            icon: route.icon,
            label: (
              <NavLink
                to={route.path}
                end
                className={() => (route.key === activeKey ? "nav-link active" : "nav-link")}
              >
                {route.label}
              </NavLink>
            ),
          };
        }),
      })),
    [activeKey],
  );

  return (
    <Menu
      mode="inline"
      selectedKeys={activeKey ? [activeKey] : []}
      defaultOpenKeys={defaultOpenKeys}
      items={menuItems}
      aria-label="主导航"
    />
  );
}

function AppLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const pageContext = getNavigationContext(location.pathname);
  const [isMobile, setIsMobile] = useState(() =>
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(max-width: 991px)").matches,
  );
  const [siderCollapsed, setSiderCollapsed] = useState(isMobile);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") {
      return;
    }
    const mediaQuery = window.matchMedia("(max-width: 991px)");
    const updateViewport = () => {
      setIsMobile(mediaQuery.matches);
      setSiderCollapsed(mediaQuery.matches);
    };
    updateViewport();
    mediaQuery.addEventListener("change", updateViewport);
    return () => mediaQuery.removeEventListener("change", updateViewport);
  }, []);

  const handleSiderBreakpoint = (broken: boolean) => {
    setIsMobile(broken);
    setSiderCollapsed(broken);
  };
  // 会话引导：交付当前用户并缓存会话级 CSRF token（写请求经其回传）。
  const session = useQuery({
    queryKey: ["session", "bootstrap"],
    queryFn: fetchSessionBootstrap,
    staleTime: 5 * 60 * 1000,
  });
  // 服务端打开页面时未认证会先 302 到登录壳；引导 401 只会出现在
  // 会话过期后：整页跳转登录壳，清空内存中的会话状态。
  useEffect(() => {
    if (session.error instanceof ApiError && session.error.status === 401) {
      window.location.assign("/admin/login");
    }
  }, [session.error, navigate]);
  const signOut = useAdminAction({
    action: () => logout(),
    successText: "已退出登录。",
    onDone: () => window.location.assign("/admin/login"),
  });
  const restart = useRestartAction();
  const accountMenuItems: MenuProps["items"] = [
    {
      key: "restart",
      icon: <ReloadOutlined />,
      label: "优雅重启",
      danger: true,
      disabled: restart.pending,
    },
    { key: "logout", icon: <LogoutOutlined />, label: "退出登录" },
  ];
  const handleAccountMenuClick: MenuProps["onClick"] = ({ key }) => {
    if (key === "restart") {
      restart.trigger();
    } else if (key === "logout") {
      void signOut.run(undefined);
    }
  };

  // 系统名称由 Go 侧 SPA 壳注入（<meta name="app-name">，settings 即时生效）：
  // 壳每次响应动态替换，挂载时同步一次浏览器标题。
  const name = appName();
  useEffect(() => {
    document.title = name;
  }, [name]);

  return (
    <Layout className="app-shell">
      <Sider
        theme="light"
        className="app-sider"
        breakpoint="lg"
        collapsible
        trigger={null}
        collapsedWidth={0}
        collapsed={siderCollapsed}
        onBreakpoint={handleSiderBreakpoint}
        onCollapse={setSiderCollapsed}
      >
        <NavLink className="brand" to="/">
          <img className="brand-mark" src="/icon.svg" alt="" aria-hidden="true" />
          <span>{name}</span>
        </NavLink>
        <Navigation />
      </Sider>
      {isMobile && !siderCollapsed ? (
        <button
          type="button"
          className="mobile-nav-backdrop"
          aria-label="关闭主导航"
          onClick={() => setSiderCollapsed(true)}
        />
      ) : null}
      <Layout className="app-main">
        <Header className="app-header">
          <button
            type="button"
            className="mobile-nav-toggle"
            aria-label={siderCollapsed ? "打开主导航" : "关闭主导航"}
            aria-expanded={!siderCollapsed}
            onClick={() => setSiderCollapsed((collapsed) => !collapsed)}
          >
            {siderCollapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
          </button>
          {pageContext?.group ? (
            <Breadcrumb
              className="app-header-breadcrumb"
              aria-label="页面路径"
              items={[
                { title: pageContext.group.label },
                ...(pageContext.parent
                  ? [{ title: <Link to={pageContext.parent.path}>{pageContext.parent.label}</Link> }]
                  : []),
                { title: pageContext.current.title },
              ]}
            />
          ) : (
            <Text strong className="app-header-title">管理端工作台</Text>
          )}
          {session.data?.authenticated ? (
            <div className="app-header-actions">
              <Dropdown
                trigger={["click"]}
                placement="bottomRight"
                menu={{ items: accountMenuItems, onClick: handleAccountMenuClick }}
              >
                <span className="account-avatar-trigger" aria-label="管理员账户菜单" role="button" tabIndex={0}>
                  <Avatar icon={<UserOutlined />} />
                </span>
              </Dropdown>
            </div>
          ) : null}
        </Header>
        <Content className="page-content">
          <AppRoutes />
        </Content>
      </Layout>
    </Layout>
  );
}

type ErrorBoundaryState = { hasError: boolean };

type ErrorBoundaryProps = { children: ReactNode };

class AppErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { hasError: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { hasError: true };
  }

  render() {
    if (this.state.hasError) {
      return <Result status="error" title="页面加载失败" subTitle="请刷新页面后重试。" />;
    }
    return this.props.children;
  }
}

/** 按路径分流布局：公开登录页独立成壳，其余路径走管理端主布局。 */
function AppShellByRoute() {
  const location = useLocation();
  if (location.pathname === "/login") {
    return <LoginPage />;
  }
  return <AppLayout />;
}

export function App() {
  return (
    <BrowserRouter basename="/admin">
      <AppErrorBoundary>
        {/* antd App 上下文：管理写操作的 message/modal 实例经它提供
            （component={false} 不渲染额外包装节点） */}
        <AntApp component={false}>
          <AppShellByRoute />
        </AntApp>
      </AppErrorBoundary>
    </BrowserRouter>
  );
}
