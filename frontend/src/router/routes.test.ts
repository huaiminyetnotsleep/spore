import { describe, expect, it } from "vitest";

import {
  getActiveNavRoute,
  getNavigationContext,
  navigationGroups,
  navRoutes,
  routeMeta,
} from "./routes";

describe("SPA 导航信息架构", () => {
  it("包含目标五组菜单和全部菜单页面", () => {
    expect(navigationGroups.map((group) => group.label)).toEqual([
      "工作台",
      "请求与频道",
      "BotUser受邀频道",
      "用户运营",
      "事件与审计",
      "系统运维",
    ]);
    expect(navRoutes).toHaveLength(19);
    expect(routeMeta).toHaveLength(22);
    expect(navigationGroups.flatMap((group) => group.routeKeys)).toEqual([
      "overview",
      "stats",
      "requests",
      "channels",
      "channel-bindings",
      "channel-settings",
      "cloud-drive",
      "invite-approvals",
      "joined-channels",
      "join-settings",
      "applications",
      "users",
      "events",
      "audit",
      "settings",
      "system-config",
      "mtproto",
      "oauth",
      "backup",
    ]);
  });

  it("工作台分组包含总览与业务统计两项", () => {
    const workspace = navigationGroups.find((group) => group.key === "workspace");
    expect(workspace?.routeKeys).toEqual(["overview", "stats"]);
    const stats = routeMeta.find((route) => route.key === "stats");
    expect(stats).toMatchObject({
      path: "/stats",
      label: "业务统计",
      groupKey: "workspace",
      menuVisible: true,
    });
    expect(getActiveNavRoute("/stats")?.key).toBe("stats");
  });

  it("详情页继承对应列表菜单，且多余路径保持 404 上下文", () => {
    expect(getActiveNavRoute("/users/123")?.key).toBe("users");
    expect(getActiveNavRoute("/requests/456")?.key).toBe("requests");
    expect(getActiveNavRoute("/channels/news")?.key).toBe("channels");
    expect(getActiveNavRoute("/users/123/extra")).toBeUndefined();
    expect(getNavigationContext("/users/123")?.parent?.key).toBe("users");
    expect(getNavigationContext("/users/123/extra")).toBeUndefined();
  });

  it("OAuth 子路由只选中 GitHub 登录页面", () => {
    expect(getActiveNavRoute("/settings/oauth")?.key).toBe("oauth");
    expect(getActiveNavRoute("/settings/oauth")?.key).not.toBe("settings");
    expect(getNavigationContext("/settings/oauth")?.group?.label).toBe("系统运维");
  });
});
