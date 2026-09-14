import { apiRequest } from "./client";

/**
 * GET /api/v1/session 返回的会话引导信息。
 * csrf_token 与 SSR 表单共用同一会话级 token；后续变更类请求
 * 通过 X-CSRF-Token 请求头回传，不引入第二套安全状态。
 */
export interface SessionBootstrap {
  authenticated: boolean;
  user: {
    name: string;
    /** 未绑定 GitHub 时省略。 */
    github_login?: string;
  };
  csrf_token: string;
  /** 会话过期时间（Unix 毫秒）；滑动续期语义由服务端维护。 */
  expires_at: number;
}

/** 会话级 CSRF token 的模块内缓存；bootstrap 成功后写入。 */
let cachedCSRFToken = "";

/** 记录会话级 CSRF token（写请求经 getCSRFToken 读取）。 */
export function setCSRFToken(token: string): void {
  cachedCSRFToken = token;
}

/** 读取会话级 CSRF token；bootstrap 未完成时为空串。 */
export function getCSRFToken(): string {
  return cachedCSRFToken;
}

/** 拉取会话引导信息；未认证时服务端返回 401 JSON（抛出 ApiError）。 */
export async function fetchSessionBootstrap(): Promise<SessionBootstrap> {
  const session = await apiRequest<SessionBootstrap>("/api/v1/session");
  setCSRFToken(session.csrf_token);
  return session;
}

// ---- 登录（SSR 登录页删除，密钥登录改走 /api/v1 JSON） ----

/** GET /api/v1/login/csrf 的响应：登录级双提交 CSRF token 与 GitHub 入口显隐。 */
export interface LoginCSRF {
  csrf_token: string;
  /** GitHub 登录通道已配置时为 true；登录页仅在 true 时展示入口链接。 */
  github_enabled: boolean;
}

/** POST /api/v1/login 的成功响应。 */
export interface LoginResult {
  ok: boolean;
}

/**
 * 取登录级 CSRF token（服务端同时种下 spore_login_csrf Cookie）。
 * 与会话级 CSRF 是两套独立 token：登录前没有会话，不能复用 bootstrap。
 */
export function fetchLoginCSRF(): Promise<LoginCSRF> {
  return apiRequest<LoginCSRF>("/api/v1/login/csrf");
}

/**
 * 访问密钥登录：token 回传到 X-CSRF-Token 头（双提交 Cookie 的另一半由
 * 浏览器自动携带）。成功后服务端签发会话 Cookie，后续刷新会话引导即可。
 */
export function loginWithAccessKey(accessKey: string, loginCSRFToken: string): Promise<LoginResult> {
  return apiRequest<LoginResult>("/api/v1/login", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": loginCSRFToken,
    },
    body: JSON.stringify({ access_key: accessKey }),
  });
}
