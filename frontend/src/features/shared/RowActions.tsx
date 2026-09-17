/**
 * 表格行操作列的统一组件：所有动作（含跳转类）都渲染为 type="link" 的
 * 链接按钮，消除裸 <Link> 文字与 <Button> 混排；跳转由调用方传 onClick
 * （内部 navigate），不再嵌套 <Link><Button /></Link>。
 *
 * 折叠规则：前 maxVisible 个动作内联展示，其余折叠进「更多」Dropdown
 * （icon-only 触发按钮，aria-label=更多操作，悬停有「更多」Tooltip，可键盘操作）。
 *
 * 折叠动作的加载态不隐藏：菜单项保持可见并禁用（label 保留）。选择这一
 * 更简单稳健的行为的原因：行动作 pending 通常伴随二次确认弹层与列表刷新，
 * 把按钮中途从菜单搬回行内会引起布局跳动，且菜单项禁用同样阻止重复提交。
 * loading 结束（或行刷新后）菜单项自动恢复。
 */
import { MoreOutlined } from "@ant-design/icons";
import { Button, Dropdown, Tooltip } from "antd";
import type { MenuProps } from "antd";
import type { ReactNode } from "react";

export interface RowActionItem {
  /** 稳定 key：折叠菜单与 React 复用都需要。 */
  key: string;
  label: ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
  loading?: boolean;
  /** 悬停说明（如禁用原因）；带 title 的内联动作经 Tooltip 展示。 */
  title?: string;
  /** 行内强调（主操作，如「同意」）。仅应出现在可见范围内；菜单中不区分 primary。 */
  primary?: boolean;
}

const MORE_TRIGGER_ARIA_LABEL = "更多操作";
const MORE_TRIGGER_TITLE = "更多";

export interface RowActionsProps {
  actions: RowActionItem[];
  /** 内联展示的最大动作数，其余折叠进「更多」菜单；0 表示全部折叠。 */
  maxVisible?: number;
}

export function RowActions({ actions, maxVisible = 2 }: RowActionsProps) {
  if (actions.length === 0) {
    return null;
  }
  const inline = actions.slice(0, maxVisible);
  const overflow = actions.slice(maxVisible);

  const menuItems: NonNullable<MenuProps["items"]> = overflow.map((item) => ({
    key: item.key,
    label: item.label,
    danger: item.danger,
    title: item.title,
    // 加载中的折叠动作保持可见并禁用（见组件头注释）
    disabled: item.disabled || item.loading,
  }));

  return (
    <div className="row-actions">
      {inline.map((item) => {
        const button = (
          <Button
            type={item.primary ? "primary" : "link"}
            size="small"
            danger={item.danger}
            loading={item.loading}
            disabled={item.disabled}
            className="row-actions__action"
            onClick={item.onClick}
          >
            {item.label}
          </Button>
        );
        // 禁用按钮不触发鼠标事件：带 title 的动作包一层 span 让 Tooltip 生效
        return item.title ? (
          <Tooltip key={item.key} title={item.title}>
            <span className="row-actions__item">{button}</span>
          </Tooltip>
        ) : (
          <span key={item.key} className="row-actions__item">
            {button}
          </span>
        );
      })}
      {overflow.length > 0 ? (
        <Dropdown
          trigger={["click"]}
          menu={{
            items: menuItems,
            onClick: ({ key }) => {
              overflow.find((item) => item.key === key)?.onClick();
            },
          }}
        >
          {/* 悬停说明用 antd Tooltip（与站内其余 Tooltip 一致），不是原生 title */}
          <Tooltip title={MORE_TRIGGER_TITLE}>
            <Button
              type="text"
              size="small"
              className="row-actions__more"
              aria-label={MORE_TRIGGER_ARIA_LABEL}
              icon={<MoreOutlined />}
            />
          </Tooltip>
        </Dropdown>
      ) : null}
    </div>
  );
}
