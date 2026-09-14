import { beforeEach, describe, expect, it } from "vitest";

import { cspConfig, readCSPNonce } from "./csp";

describe("SPA CSP nonce", () => {
  beforeEach(() => {
    document.head.innerHTML = "";
  });

  it("读取 Go 入口注入的 nonce 并传给 ConfigProvider 配置", () => {
    const meta = document.createElement("meta");
    meta.name = "csp-nonce";
    meta.content = "nonce-from-go";
    document.head.append(meta);

    const nonce = readCSPNonce();
    expect(nonce).toBe("nonce-from-go");
    expect(cspConfig(nonce)).toEqual({ nonce: "nonce-from-go" });
  });

  it("开发服务器没有 nonce 时保持无 CSP 配置回退", () => {
    const meta = document.createElement("meta");
    meta.name = "csp-nonce";
    meta.content = "__SPORE_CSP_NONCE__";
    document.head.append(meta);

    expect(readCSPNonce()).toBeUndefined();
    expect(cspConfig(undefined)).toBeUndefined();
  });
});
