/**
 * 受邀设置页（/join）：从「运行设置」拆出的受邀频道域配置页。
 * 按功能分区：功能开关（总开关）、加入策略（审核/数量上限）、加入后处理
 * （静音/归档）、外部频道治理（自动退出外部拉入）；每项切换即保存、即时生效
 * （joinmgr 每次实时读取 syscfg）；上限为数字输入，需显式点「保存上限」。
 */
import { useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, InputNumber, Space, Switch, Tag, Typography } from "antd";
import { Link } from "react-router-dom";

import { fetchSettings } from "../../api/admin";
import { saveSettings, type SettingsSaveInput } from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { LoadError, PageCard, SectionCard } from "../shared/PageStates";

const { Text } = Typography;

/** 单条配置行：控件 + 标签在上，说明文字在下，条目之间留出呼吸空间。 */
function SettingItem({
  control,
  label,
  description,
  extra,
}: {
  control: ReactNode;
  label: string;
  description?: ReactNode;
  extra?: ReactNode;
}) {
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

export function JoinSettingsPage() {
  // 加入上限的本地编辑值；undefined = 未修改（跟随服务端值展示）
  const [joinMaxInput, setJoinMaxInput] = useState<number | undefined>(undefined);
  const { data, isError, refetch } = useQuery({
    queryKey: ["settings"],
    queryFn: fetchSettings,
  });

  // 频道加入配置：每项切换即保存，即时生效；
  // join_max_channels 需显式点保存（数字输入不宜每键触发）。
  const saveJoinConfig = useAdminAction({
    action: (input: SettingsSaveInput) => saveSettings(input),
    invalidate: [["settings"], ["channel-join"]],
    successText: "频道加入配置已保存，即时生效。",
  });
  const saveJoinMax = useAdminAction({
    action: (maxChannels: number | null) =>
      saveSettings({ join_max_channels: maxChannels ?? 0 }),
    invalidate: [["settings"], ["channel-join"]],
    successText: (result) =>
      result.settings.join_max_channels === 0
        ? "已改为不限制加入数量。"
        : `加入数量上限已设为 ${result.settings.join_max_channels}。`,
  });

  if (isError) {
    return (
      <PageCard title="受邀设置（/join）">
        <LoadError onRetry={() => void refetch()} />
      </PageCard>
    );
  }

  return (
    <PageCard title="受邀设置（/join）">
      <Space direction="vertical" size="middle" className="field-width-full">
        <Alert type="info" showIcon message="每项配置切换即保存并即时生效，无需重启。" />

        <SectionCard title="功能开关">
          <SettingItem
            control={
              <Switch
                checked={data?.join_enabled ?? false}
                loading={saveJoinConfig.pending}
                onChange={(enabled) => void saveJoinConfig.run({ join_enabled: enabled })}
                aria-label="允许加入频道总开关"
              />
            }
            label="允许通过 /join 邀请链接加入频道"
            extra={(data?.join_enabled ?? false) ? <Tag color="green">已开启</Tag> : <Tag>默认关闭</Tag>}
            description={'号主提交邀请链接即时加入；其他用户提交进入审批列表。总开关关闭时 /join 直接回复"功能已关闭"。'}
          />
        </SectionCard>

        <SectionCard title="加入策略">
          <Space direction="vertical" size="middle" className="field-width-full">
            <SettingItem
              control={
                <Switch
                  checked={data?.join_require_approval ?? true}
                  loading={saveJoinConfig.pending}
                  onChange={(enabled) => void saveJoinConfig.run({ join_require_approval: enabled })}
                  aria-label="加入需审核开关"
                />
              }
              label="普通用户提交需号主审核"
            />
            <SettingItem
              control={
                <InputNumber
                  min={0}
                  max={200}
                  value={joinMaxInput ?? data?.join_max_channels ?? 20}
                  disabled={saveJoinMax.pending}
                  onChange={(v) => setJoinMaxInput(typeof v === "number" ? v : 0)}
                  className="field-width-120"
                  aria-label="加入数量上限"
                />
              }
              label="加入数量上限（0 = 不限，0–200）"
              extra={
                <Button
                  size="small"
                  loading={saveJoinMax.pending}
                  disabled={joinMaxInput === undefined || joinMaxInput === data?.join_max_channels}
                  onClick={() => joinMaxInput !== undefined && void saveJoinMax.run(joinMaxInput)}
                >
                  保存上限
                </Button>
              }
            />
            <Text type="secondary" className="layout-margin-block-end-0">
              加入申请的审批在<Link to="/invite-approvals">加入审批</Link>页维护。
            </Text>
          </Space>
        </SectionCard>

        <SectionCard title="加入后处理">
          <Space direction="vertical" size="middle" className="field-width-full">
            <SettingItem
              control={
                <Switch
                  checked={data?.join_mute_enabled ?? true}
                  loading={saveJoinConfig.pending}
                  onChange={(enabled) => void saveJoinConfig.run({ join_mute_enabled: enabled })}
                  aria-label="加入后静音开关"
                />
              }
              label="加入后自动静音该频道"
            />
            <SettingItem
              control={
                <Switch
                  checked={data?.join_archive_enabled ?? true}
                  loading={saveJoinConfig.pending}
                  onChange={(enabled) => void saveJoinConfig.run({ join_archive_enabled: enabled })}
                  aria-label="加入后归档开关"
                />
              }
              label="加入后自动归档对话"
            />
          </Space>
        </SectionCard>

        <SectionCard title="外部频道治理">
          <Space direction="vertical" size="middle" className="field-width-full">
            <SettingItem
              control={
                <Switch
                  checked={data?.join_auto_leave_external ?? false}
                  loading={saveJoinConfig.pending}
                  onChange={(enabled) => void saveJoinConfig.run({ join_auto_leave_external: enabled })}
                  aria-label="自动退出外部拉入频道开关"
                />
              }
              label="自动退出外部拉入的频道"
              description={'开启后，在「已加入频道」页刷新或 /join 提交时，检测到非本系统加入的频道会自动退出（Telegram 不提供"拒绝被拉入"开关，只能事后拦截）。默认关闭，可随时在该页手动退出。'}
            />
            <Text type="secondary" className="layout-margin-block-end-0">
              账号已加入的频道（含退出）在<Link to="/joined-channels">已加入频道</Link>页查看。
            </Text>
          </Space>
        </SectionCard>
      </Space>
    </PageCard>
  );
}
