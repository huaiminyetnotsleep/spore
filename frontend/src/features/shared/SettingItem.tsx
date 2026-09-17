import { Space, Typography } from "antd";
import type { ReactNode } from "react";

const { Text } = Typography;

export interface SettingItemProps {
  /** 行内设置控件（Switch、InputNumber、Space.Compact 等）。 */
  control: ReactNode;
  /** 控件右侧的设置名称。 */
  label: ReactNode;
  /** 控件旁的状态标记（如「已开启」Tag）或紧邻控件的单字段保存按钮。 */
  extra?: ReactNode;
  /** 设置行为说明，展示在控件行下方（即时生效/显式保存等语义在这里说明）。 */
  description?: ReactNode;
}

/**
 * 设置行的稳定形态：控件 + 标签（+ 状态/行内动作）在上，说明文字在下。
 * 只负责排布；保存语义（自动保存/显式保存）由调用方通过控件与文案表达。
 */
export function SettingItem({ control, label, extra, description }: SettingItemProps) {
  return (
    <Space direction="vertical" size={2} className="field-width-full">
      <Space wrap>
        {control}
        <Text>{label}</Text>
        {extra}
      </Space>
      {description ? (
        <Text type="secondary" className="layout-margin-block-end-0">
          {description}
        </Text>
      ) : null}
    </Space>
  );
}
