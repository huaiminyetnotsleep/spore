import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchSessionBootstrap } from "./session";

const bootstrapPayload = {
  authenticated: true,
  user: { name: "admin" },
  csrf_token: "csrf-token-value",
  expires_at: 1756598400000,
};

describe("session bootstrap API", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("以同源凭据请求 /api/v1/session 并解析引导信息", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(bootstrapPayload), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const session = await fetchSessionBootstrap();

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/session",
      expect.objectContaining({ credentials: "same-origin" }),
    );
    expect(session.authenticated).toBe(true);
    expect(session.user.name).toBe("admin");
    expect(session.csrf_token).toBe("csrf-token-value");
    expect(session.expires_at).toBe(1756598400000);
  });
});
