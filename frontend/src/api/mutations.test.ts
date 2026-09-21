import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  addWatchSource,
  approveWatchInviteRequest,
  deleteWatchInviteRequest,
  rejectWatchInviteRequest,
  retryWatchInviteRequest,
} from "./mutations";
import { setCSRFToken } from "./session";

function okResponse() {
  return new Response(JSON.stringify({ ok: true }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

describe("watch source mutations", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation(async () => okResponse());
    vi.stubGlobal("fetch", fetchMock);
    setCSRFToken("watch-csrf");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    setCSRFToken("");
  });

  it("允许 add target 提交邀请链接并保持原有 enabled 请求体", async () => {
    await addWatchSource("https://t.me/+InviteHash", false);

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/watch-sources/add",
      expect.objectContaining({
        method: "POST",
        credentials: "same-origin",
        body: JSON.stringify({ target: "https://t.me/+InviteHash", enabled: false }),
      }),
    );
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("watch-csrf");
  });

  it("调用邀请申请 approve/reject/retry/delete 端点", async () => {
    await approveWatchInviteRequest(11);
    await rejectWatchInviteRequest(12);
    await retryWatchInviteRequest(13);
    await deleteWatchInviteRequest(14);

    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/watch-invite-requests/11/approve",
      "/api/v1/watch-invite-requests/12/reject",
      "/api/v1/watch-invite-requests/13/retry",
      "/api/v1/watch-invite-requests/14/delete",
    ]);
    for (const [, init] of fetchMock.mock.calls) {
      expect(init).toEqual(expect.objectContaining({ method: "POST", body: "{}" }));
    }
  });
});

describe("watch events mutations", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation(async () =>
      new Response(JSON.stringify({ ok: true, deleted: 2 }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    setCSRFToken("watch-events-csrf");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    setCSRFToken("");
  });

  it("批量删除预热事件提交 ids 到 delete 端点", async () => {
    const { deleteWatchEvents } = await import("./mutations");
    const result = await deleteWatchEvents([3, 7]);

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/watch-events/delete",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ ids: [3, 7] }),
      }),
    );
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("watch-events-csrf");
    expect(result.deleted).toBe(2);
  });
});
