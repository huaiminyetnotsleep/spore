/**
 * 监听源配置页（/watch-settings）：从监听源管理拆出的专属配置页。
 * 管理用户经 Bot /watch 自助申请的开关、审批策略与数量上限。
 * 开关类配置切换即保存、即时生效；总数上限与每用户上限需显式点击保存。
 */
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, InputNumber, Space, Switch, Tag, Typography } from "antd";

import { fetchSettings } from "../../api/admin";
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

  const { data: cfg, isPending, isError, refetch } = useQuery({
    queryKey: ["settings"],
    queryFn: fetchSettings,
  });

  const saveApply = useAdminAction({
    action: (enabled: boolean) => saveSettings({ watch_apply_enabled: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.watch_apply_enabled
        ? "用户自助申请已开放（按当前审批模式与上限执行）。"
        : "用户自助申请已关闭（管理员添加不受影响）。",
  });

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
      description="配置用户经 Bot /watch 自助申请监听源的开关、审批策略与数量上限；开关类配置切换即保存，上限需显式保存。"
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
        </Space>
      </PageQueryState>
    </PageScaffold>
  );
}
