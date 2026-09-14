import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { MetricGrid } from "./MetricGrid";

describe("MetricGrid KPI 指标卡", () => {
  it("渲染标签、数值、后缀与语义色调类；指标单行等宽", () => {
    const { container } = render(
      <MetricGrid
        items={[
          { label: "成功", value: 9, tone: "success" },
          { label: "已加入频道", value: 11, suffix: " / 20", tone: "success" },
        ]}
      />,
    );

    // 列数修饰类 = 指标数量（桌面端固定单行）
    expect(container.querySelector(".metric-grid")?.className).toContain("metric-grid--2");
    expect(screen.getByText("成功").closest(".metric-card")?.className).toContain(
      "metric-card--success",
    );
    expect(screen.getByText("9")).toBeInTheDocument();
    expect(screen.getByText("11")).toBeInTheDocument();
    expect(screen.getByText("/ 20").className).toContain("metric-card__suffix");
    // 未提供 progress 的指标不渲染进度条
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("进度按分段格子点亮：0.5 → 12 格中亮 6 格，缺省/越界不渲染", () => {
    render(
      <MetricGrid
        items={[
          { label: "成功率", value: "50.0%", progress: 0.5, tone: "success" },
          { label: "请求总数", value: 12 },
          { label: "错误率", value: "—", progress: Number.NaN },
        ]}
      />,
    );

    const bar = screen.getByRole("progressbar", { name: "成功率" });
    const cells = bar.querySelectorAll(".metric-card__bar-cell");
    expect(cells.length).toBe(12);
    expect(bar.querySelectorAll(".metric-card__bar-cell--on").length).toBe(6);
    expect(screen.queryByRole("progressbar", { name: "请求总数" })).not.toBeInTheDocument();
    expect(screen.queryByRole("progressbar", { name: "错误率" })).not.toBeInTheDocument();
  });
});
