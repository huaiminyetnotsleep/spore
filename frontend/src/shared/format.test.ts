import { describe, expect, it } from "vitest";

import {
  DELIVERY_MODE_LABELS,
  ERROR_CODE_LABELS,
  errorCodeLabel,
  joinSourceLabel,
  labelOf,
} from "./format";

describe("errorCodeLabel", () => {
  it("已知错误码返回中文标签", () => {
    expect(errorCodeLabel("MEDIA_DOWNLOAD_FAILED")).toBe("媒体下载失败");
    expect(errorCodeLabel("NETWORK_ERROR")).toBe("网络故障");
    expect(errorCodeLabel("SEND_TARGET_INVALID")).toBe("发送目标不可用");
    expect(errorCodeLabel("CHANNEL_INVITE_INVALID")).toBe("绑定邀请无效");
  });

  it("未知错误码回退原值（历史/新增码兜底）", () => {
    expect(errorCodeLabel("SOME_FUTURE_CODE")).toBe("SOME_FUTURE_CODE");
  });

  it("错误码标签表无重复值，保证检索选项一一对应", () => {
    const labels = Object.values(ERROR_CODE_LABELS);
    expect(new Set(labels).size).toBe(labels.length);
  });
});

describe("labelOf", () => {
  it("投递方式标签映射生效，未知值回退原值", () => {
    expect(labelOf(DELIVERY_MODE_LABELS, "upload")).toBe("媒体投递");
    expect(labelOf(DELIVERY_MODE_LABELS, "other_mode")).toBe("other_mode");
  });

  it("绑定邀请解析来源显示中文标签", () => {
    expect(joinSourceLabel("bind_resolve")).toBe("绑定邀请解析");
  });
});
