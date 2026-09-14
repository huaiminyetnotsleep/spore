/**
 * 系统名称（品牌名）读取：Go 侧 SPA 壳每次响应把 settings 中可配置的名称
 * 注入 <meta name="app-name">（登录页未认证同样注入，管理端改名后刷新即
 * 生效）；meta 缺失、为空或仍是注入占位符（纯前端 dev server）时回退缺省值。
 */
import { PROJECT_IDENTITY } from "./projectIdentity.generated";

export const DEFAULT_APP_NAME = PROJECT_IDENTITY.displayName;

const APP_NAME_PLACEHOLDER = PROJECT_IDENTITY.appNamePlaceholder;

/** 读取当前系统名称；SSR/无文档环境返回缺省值。 */
export function appName(): string {
  if (typeof document === "undefined") {
    return DEFAULT_APP_NAME;
  }
  const raw = document
    .querySelector('meta[name="app-name"]')
    ?.getAttribute("content")
    ?.trim();
  if (!raw || raw === APP_NAME_PLACEHOLDER) {
    return DEFAULT_APP_NAME;
  }
  return raw;
}

/** 品牌角标字母：取系统名称首字符大写（名称异常时回退缺省首字母）。 */
export function brandMark(name: string = appName()): string {
  const initial = name.charAt(0) || DEFAULT_APP_NAME.charAt(0);
  return initial.toUpperCase();
}
