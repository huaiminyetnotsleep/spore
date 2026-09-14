import { afterEach, describe, expect, it } from "vitest";

import { appName, brandMark, DEFAULT_APP_NAME } from "./appName";

function setMeta(content: string | null) {
  document.querySelectorAll('meta[name="app-name"]').forEach((el) => el.remove());
  if (content !== null) {
    const meta = document.createElement("meta");
    meta.name = "app-name";
    meta.setAttribute("content", content);
    document.head.appendChild(meta);
  }
}

afterEach(() => {
  setMeta(null);
});

describe("appName", () => {
  it("meta 缺失时回退缺省值", () => {
    setMeta(null);
    expect(appName()).toBe(DEFAULT_APP_NAME);
  });

  it("读取壳注入的系统名称并去空白", () => {
    setMeta("  我的提取站  ");
    expect(appName()).toBe("我的提取站");
  });

  it("占位符（Go 注入失败残留）按缺省值兜底", () => {
    setMeta("__SPORE_APP_NAME__");
    expect(appName()).toBe(DEFAULT_APP_NAME);
  });

  it("brandMark 取名称首字符大写", () => {
    expect(brandMark("spore")).toBe("S");
    expect(brandMark("我的提取站")).toBe("我");
    expect(brandMark("")).toBe("S");
  });
});
