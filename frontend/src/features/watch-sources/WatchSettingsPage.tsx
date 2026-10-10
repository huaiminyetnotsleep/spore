/**
 * 监听源配置页（/watch-settings）：从监听源管理拆出的专属配置页。
 * 管理用户经 Bot /watch 自助申请的开关、审批策略与数量上限，以及监听
 * 转发频道（独立于缓存频道的镜像目标）。开关类配置切换即保存、即时生效；
 * 总数/每用户上限与转发频道需显式点击保存。
 */
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, InputNumber, Select, Space, Switch, Tag, Typography } from "antd";

import { fetchChannelBindings, fetchSettings } from "../../api/admin";
import { saveSettings } from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { SettingItem } from "../shared/SettingItem";

const { Text } = Typography;

export function WatchSettingsPage() {
  const [maxSources, setMaxSources] = useState<number | null>(null);
  const [perUser, setPerUser] = useState<number | null>(null);
  // 监听转发频道：草稿态目标列表（字符串形态，保存时整体替换并逐项校验
  // 解析）。null = 未开始编辑（跟随服务端值）；既有条目以数字 ID 字符串
  // 形态参与编辑（服务端按数字 ID 解析，频道公开转私有不受影响）。
  const [draftForwards, setDraftForwards] = useState<string[] | null>(null);

  const { data: cfg, isPending, isError, refetch } = useQuery({
    queryKey: ["settings"],
    queryFn: fetchSettings,
  });
  // 已绑定的频道/群组（active）：转发频道的下拉枚举候选；也支持直接输入
  // 任意目标（tags 模式）。查询失败不阻塞配置（下拉无候选，输入仍可用）。
  const bindings = useQuery({ queryKey: ["channel-bindings"], queryFn: () => fetchChannelBindings() });

  const saveApply = useAdminAction({
    action: (enabled: boolean) => saveSettings({ watch_apply_enabled: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.watch_apply_enabled
        ? "用户自助申请已开放（按当前审批模式与上限执行）。"
        : "用户自助申请已关闭（管理员添加不受影响）。",
  });

  const saveForwards = useAdminAction({
    action: (targets: string[]) => saveSettings({ watch_forward_channels: targets }),
    invalidate: [["settings"]],
    successText: (result) => {
      const saved = result.settings.watch_forward_channels?.length ?? 0;
      return saved > 0
        ? `已保存 ${saved} 个监听转发频道（即时生效）。`
        : "已清空监听转发频道（监听回落仅缓存频道兜底）。";
    },
    onDone: () => setDraftForwards(null),
  });

  const serverForwards = useMemo(
    () => (cfg?.watch_forward_channels ?? []).map((c) => String(c.channel_id)),
    [cfg],
  );
  const forwards = draftForwards ?? serverForwards;
  const forwardsDirty = draftForwards != null && draftForwards.join("\n") !== serverForwards.join("\n");

  // 下拉候选：已绑定频道/群组（active）按数字 ID 去重；值为 -100 数字 ID
  // 字符串（服务端可直接解析校验），label 带标题与 @用户名便于辨认。
  const boundChannelOptions = useMemo(() => {
    const seen = new Set<number>();
    const options: { value: string; label: string }[] = [];
    for (const row of bindings.data?.items ?? []) {
      if (row.status !== "active" || seen.has(row.channel_id)) {
        continue;
      }
      seen.add(row.channel_id);
      options.push({
        value: String(row.channel_id),
        label: row.username ? `${row.title}（@${row.username}）` : row.title,
      });
    }
    return options;
  }, [bindings.data]);

  const resetDraftForwards = () => {
    setDraftForwards(null);
  };

  const saveApproval = useAdminAction({
    action: (enabled: boolean) => saveSettings({ watch_require_approval: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.watch_require_approval
        ? "用户申请需审批后生效。"
        : "用户申请免审批，直接生效。",
  });

  const saveLimits = useAdminAction({
    action: (input: { max: number; perUser: number }) =>
      saveSettings({ watch_max_sources: input.max, watch_per_user_limit: input.perUser }),
    invalidate: [["settings"]],
    successText: () => "监听源上限已更新，即时生效。",
  });

  const currentMax = maxSources ?? cfg?.watch_max_sources ?? 20;
  const currentPerUser = perUser ?? cfg?.watch_per_user_limit ?? 3;
  const limitsDirty =
    cfg != null &&
    (currentMax !== cfg.watch_max_sources || currentPerUser !== cfg.watch_per_user_limit);

  return (
    <PageScaffold
      title="监听源配置"
      description="配置用户经 Bot /watch 自助申请监听源的开关、审批策略与数量上限，并管理监听转发频道（独立于缓存频道的镜像目标）；开关类配置切换即保存，上限与转发频道需显式保存。"
    >
      <PageQueryState
        initialLoading={isPending && !cfg}
        error={isError && !cfg}
        hasData={!!cfg}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="middle" className="field-width-full">
          <PageSection title="用户自助申请（Bot /watch）" extra={<Tag color="green">切换即保存</Tag>}>
            <Space direction="vertical" size="small" className="field-width-full">
              <SettingItem
                control={
                  <Switch
                    checked={cfg?.watch_apply_enabled ?? false}
                    loading={saveApply.pending}
                    onChange={(enabled) => void saveApply.run(enabled)}
                    aria-label="用户自助申请开关"
                  />
                }
                label="允许用户经 /watch 自助申请监听源"
                description="仅已通过 /start 审核的用户可申请；管理员在「监听源管理」直接添加不受此开关限制。默认关闭。"
              />
              <SettingItem
                control={
                  <Switch
                    checked={cfg?.watch_require_approval ?? true}
                    loading={saveApproval.pending}
                    onChange={(enabled) => void saveApproval.run(enabled)}
                    aria-label="申请审批开关"
                  />
                }
                label="用户申请需审批后生效"
                description="开启后申请进入「监听源管理」列表「待审批」；关闭则免审批直接生效。号主经 Bot 提交始终直接生效。"
              />
              <Space size="large">
                <SettingItem
                  control={
                    <InputNumber
                      value={maxSources ?? cfg?.watch_max_sources ?? 20}
                      min={0}
                      max={200}
                      precision={0}
                      onChange={(v) => setMaxSources(v)}
                      aria-label="监听源总数上限"
                    />
                  }
                  label="总数上限（0 不限）"
                />
                <SettingItem
                  control={
                    <InputNumber
                      value={perUser ?? cfg?.watch_per_user_limit ?? 3}
                      min={0}
                      max={20}
                      precision={0}
                      onChange={(v) => setPerUser(v)}
                      aria-label="每用户申请上限"
                    />
                  }
                  label="每用户上限（0 不限）"
                />
              </Space>
              <FormActions>
                <Button
                  loading={saveLimits.pending}
                  disabled={!limitsDirty}
                  onClick={() => {
                    void saveLimits.run({ max: currentMax, perUser: currentPerUser });
                  }}
                >
                  保存上限
                </Button>
              </FormActions>
              <Text type="secondary">
                上限只约束用户申请（含待审批），管理员添加与号主提交不受限；0 表示不限制。即时生效。
              </Text>
            </Space>
          </PageSection>

          <PageSection title="监听转发频道" extra={<Tag color="blue">保存后生效</Tag>}>
            <Space direction="vertical" size="small" className="field-width-full">
              <Select
                mode="tags"
                className="field-width-full"
                value={forwards}
                onChange={(values) => setDraftForwards(values)}
                options={boundChannelOptions}
                placeholder="从已绑定的频道/群组中选择，或直接输入 @用户名 / t.me 链接 / -100 数字 ID"
                aria-label="监听转发频道"
                maxCount={10}
                tokenSeparators={[","]}
                loading={bindings.isPending}
              />
              <FormActions>
                <Button
                  type="primary"
                  loading={saveForwards.pending}
                  disabled={!forwardsDirty}
                  onClick={() => void saveForwards.run(forwards)}
                >
                  保存转发频道
                </Button>
                <Button disabled={!forwardsDirty} onClick={resetDraftForwards}>
                  重置
                </Button>
              </FormActions>
              <Text type="secondary">
                下拉枚举已绑定的频道/群组，也支持直接输入任意目标。转发频道独立于缓存频道：监听消息始终预热缓存频道，并原样转发一份到这里的每个频道（媒体/相册/caption
                完整、无脚注）；受保护内容（禁止转发的源）经缓存频道中转后同样进入转发频道。每个受理
                bot 都需被设为转发频道管理员（可发帖）；最多 10 个，保存时逐个校验解析，既有条目以数字
                ID 形态展示。使用前需先配置缓存频道（「请求与频道 → 频道设置」）。
              </Text>
            </Space>
          </PageSection>
        </Space>
      </PageQueryState>
    </PageScaffold>
  );
}
