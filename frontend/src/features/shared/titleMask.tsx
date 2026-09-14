/**
 * 频道名称脱敏开关（加入审批页 / 已加入频道页共用）：
 * 敏感频道名默认暗文（***），经顶部按钮一键显示 / 隐藏明文。
 * 状态仅存内存——每次进入页面恢复默认暗文，避免明文长期停留在屏幕上。
 */
import type { ReactElement } from "react";
import { useState } from "react";
import { Button } from "antd";
import { EyeInvisibleOutlined, EyeOutlined } from "@ant-design/icons";

export function useTitleMask() {
  const [revealed, setRevealed] = useState(false);
  const toggle: ReactElement = (
    <Button
      icon={revealed ? <EyeInvisibleOutlined /> : <EyeOutlined />}
      onClick={() => setRevealed((v) => !v)}
    >
      {revealed ? "隐藏明文" : "显示明文"}
    </Button>
  );
  // 空标题原样返回，交由调用方的「未知标题」兜底展示
  const text = (v: string) => (revealed ? v : v ? "***" : v);
  return { revealed, toggle, text };
}
