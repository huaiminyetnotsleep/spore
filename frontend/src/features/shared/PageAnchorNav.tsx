import { Anchor } from "antd";
import type { AnchorProps } from "antd";

export type PageAnchorItems = NonNullable<AnchorProps["items"]>;

export interface PageAnchorNavProps {
  items: PageAnchorItems;
}

// 滚动容器是 AppLayout 的 .page-content（应用壳锁定视口、仅内容区滚动），
// Anchor 默认监听 window 不会生效；单测独立渲染页面时没有该节点，回退 window。
function scrollContainer(): HTMLElement | Window {
  return document.querySelector<HTMLElement>(".page-content") ?? window;
}

/** 页面区块目录导航：配合 .page-anchor-rail（sticky）使用，窄屏由 CSS 隐藏。 */
export function PageAnchorNav({ items }: PageAnchorNavProps) {
  return (
    <Anchor
      items={items}
      affix={false}
      replace
      offsetTop={16}
      getContainer={scrollContainer}
    />
  );
}
