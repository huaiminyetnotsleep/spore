/**
 * 同源 API 请求边界。
 * 这里只处理请求、响应和错误归一化，不定义任何业务接口。
 * 错误响应按 /api/v1 统一信封 {"error":{code,message}} 解析：
 * message 是服务端受控中文文案（apperr 同源），供页面直接展示。
 */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code = "",
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** 服务端不可用/非 JSON 错误体的兜底文案（不含底层细节）。 */
function fallbackError(status: number): ApiError {
  return new ApiError(`API 请求失败（${status}）`, status);
}

/** 把非 2xx 响应归一为 ApiError；信封缺失时回退通用文案。 */
export async function apiErrorFrom(response: Response): Promise<ApiError> {
  const contentType = response.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) {
    return fallbackError(response.status);
  }
  try {
    const payload = (await response.json()) as {
      error?: { code?: string; message?: string };
    };
    if (!payload.error) {
      return fallbackError(response.status);
    }
    return new ApiError(
      payload.error.message ?? `API 请求失败（${response.status}）`,
      response.status,
      payload.error.code ?? "",
    );
  } catch {
    return fallbackError(response.status);
  }
}

export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (!headers.has("Accept")) {
    headers.set("Accept", "application/json");
  }

  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers,
  });

  if (!response.ok) {
    throw await apiErrorFrom(response);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const contentType = response.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    return (await response.json()) as T;
  }

  return (await response.text()) as T;
}
