import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./client";
import { controlRecoveryJob, createRecoveryJob, fetchRecoveryItems, fetchRecoveryJob, fetchRecoveryJobs, previewRecovery, type RecoveryInput } from "./recovery";
import { setCSRFToken } from "./session";

const input: RecoveryInput = { filter: { channel_key: "-1001234567890", user_id: 123, since: 1700000000000, until: 1700000001234 }, target: "@target", bot_id: 42 };
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}
describe("recovery API", () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation(async () => response({ id: 71 }));
    vi.stubGlobal("fetch", fetchMock);
    setCSRFToken("recovery-csrf");
  });
  afterEach(() => { vi.unstubAllGlobals(); setCSRFToken(""); });
  it("preview and create share the exact request body, Unix ms and session CSRF", async () => {
    fetchMock.mockResolvedValueOnce(response({ total: 2, bot_id: 42 }));
    expect(await previewRecovery(input)).toEqual({ total: 2, bot_id: 42 });
    expect(await createRecoveryJob(input)).toEqual({ id: 71 });
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(["/api/v1/recovery/preview", "/api/v1/recovery/jobs"]);
    for (const [, init] of fetchMock.mock.calls) {
      expect(init).toMatchObject({ method: "POST", credentials: "same-origin", body: JSON.stringify(input) });
      expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("recovery-csrf");
    }
  });
  it("reads list envelopes and raw detail, preserving pagination and status", async () => {
    const envelope = { items: [], page: 2, page_size: 10, total: 0, total_pages: 0 };
    fetchMock.mockResolvedValueOnce(response(envelope));
    expect(await fetchRecoveryJobs(2, 10)).toEqual(envelope);
    expect(await fetchRecoveryJob(71)).toEqual({ id: 71 });
    await fetchRecoveryItems(71, 3, 50, "uncertain");
    await fetchRecoveryItems(71);
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/recovery/jobs?page=2&page_size=10", "/api/v1/recovery/jobs/71",
      "/api/v1/recovery/jobs/71/items?page=3&page_size=50&status=uncertain", "/api/v1/recovery/jobs/71/items?page=1&page_size=20",
    ]);
  });
  it("all control endpoints return singleton jobs and carry CSRF", async () => {
    for (const action of ["pause", "resume", "cancel", "retry"] as const) {
      expect(await controlRecoveryJob(71, action)).toEqual({ id: 71 });
    }
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(["pause", "resume", "cancel", "retry"].map((action) => `/api/v1/recovery/jobs/71/${action}`));
    for (const [, init] of fetchMock.mock.calls) {
      expect(init.method).toBe("POST");
      expect(init.body).toBe("{}");
      expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("recovery-csrf");
    }
  });
  it.each([403, 409, 503])("preserves controlled errors (%s)", async (status) => {
    fetchMock.mockResolvedValue(response({ error: { code: "RECOVERY_CONFLICT", message: "请重新预检。" } }, status));
    await expect(createRecoveryJob(input)).rejects.toEqual(new ApiError("请重新预检。", status, "RECOVERY_CONFLICT"));
  });
});
