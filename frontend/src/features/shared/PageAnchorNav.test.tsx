import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PageAnchorNav } from "./PageAnchorNav";

describe("PageAnchorNav 页面锚点目录", () => {
  it("按 items 渲染目录链接；独立渲染（无 .page-content 祖先）时回退 window 不崩溃", () => {
    render(
      <PageAnchorNav
        items={[
          { key: "a", href: "#a", title: "区块 A" },
          { key: "b", href: "#b", title: "区块 B" },
        ]}
      />,
    );

    expect(screen.getByRole("link", { name: "区块 A" })).toHaveAttribute("href", "#a");
    expect(screen.getByRole("link", { name: "区块 B" })).toHaveAttribute("href", "#b");
  });
});
