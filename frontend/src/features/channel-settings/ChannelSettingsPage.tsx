/**
 * 频道设置页（请求与频道分组的配置入口）：从「运行设置」拆出，
 * 承载副本同步开关与缓存频道（重复链接复用）配置区，后续频道相关运行
 * 配置可继续归入本页分区。
 * 副本同步总开关（channel_copy_enabled）：切换即保存、即时生效；
 * 关闭只暂停任务成功后的副本投递，用户绑定关系保留（「频道绑定」页维护）。
 * 缓存频道（dump_channel）：输入 @用户名 / t.me 链接 / -100 数字 ID，保存时
 * 经服务端解析并校验 bot 已是频道管理员，存数字 ID（频道之后公开转私有不
 * 影响）；清空即关闭复用。即时生效，无需重启。复用总开关
 * （tg_reuse_enabled）保留在缓存频道分区；重复链接检测窗口
 * （dedup_window_min）使用独立卡片。
 */
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Input, InputNumber, Space, Switch, Tag, Typography } from "antd";

import { fetchSettings } from "../../api/admin";
import { saveSettings } from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { LoadError, PageCard, SectionCard } from "../shared/PageStates";

const { Text } = Typography;

export function ChannelSettingsPage() {
  const { data, isError, refetch } = useQuery({
    queryKey: ["settings"],
    queryFn: fetchSettings,
  });
  const [dumpTarget, setDumpTarget] = useState("");
  const [dedupMin, setDedupMin] = useState<number | null>(null);

  // 回填：设置数据异步返回后显式同步检测窗口输入框
  useEffect(() => {
    if (data) {
      setDedupMin(data.dedup_window_min);
    }
  }, [data]);

  // 频道同步开关：切换即保存，即时生效；
  // 关闭只暂停副本投递，用户绑定关系保留。
  const saveChannelCopy = useAdminAction({
    action: (enabled: boolean) => saveSettings({ channel_copy_enabled: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.channel_copy_enabled
        ? "频道同步已开启，任务成功后会同步到用户绑定的频道。"
        : "频道同步已关闭，绑定关系保留；重新打开即恢复。",
  });

  // 缓存频道：保存（非空=解析校验后配置；空串=清除）。服务端校验失败回受控 400。
  const saveDumpChannel = useAdminAction({
    action: (value: string) => saveSettings({ dump_channel: value.trim() }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.dump_channel_id !== 0
        ? `缓存频道已配置（${result.settings.dump_channel_title || result.settings.dump_channel_id}），重复链接复用即时生效。`
        : "缓存频道已清除，重复链接回到完整下载上传。",
    onDone: () => setDumpTarget(""),
  });

  // 复用总开关：切换即保存，即时生效；关闭回到完整下载上传，副本保留。
  const saveReuse = useAdminAction({
    action: (enabled: boolean) => saveSettings({ tg_reuse_enabled: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.tg_reuse_enabled
        ? "链接复用已开启，重复链接将直接复制缓存频道副本，不再重复下载上传。"
        : "链接复用已关闭，重复链接回到完整下载上传；重新打开即恢复。",
  });

  // 重复链接检测窗口：显式保存（1–1440 分钟，服务端复核）
  const saveDedup = useAdminAction({
    action: (minutes: number) => saveSettings({ dedup_window_min: minutes }),
    invalidate: [["settings"]],
    successText: () => "重复链接检测窗口已更新，即时生效。",
  });

  if (isError) {
    return (
      <PageCard title="频道设置">
        <LoadError onRetry={() => void refetch()} />
      </PageCard>
    );
  }

  const dedupDirty =
    dedupMin != null && data != null && dedupMin !== data.dedup_window_min;

  return (
    <PageCard title="频道设置">
      <Space direction="vertical" size="middle" className="field-width-full">
      <SectionCard title="副本同步">
        <Space direction="vertical" size="small" className="field-width-full">
          <Space>
            <Switch
              checked={data?.channel_copy_enabled ?? true}
              loading={saveChannelCopy.pending}
              onChange={(enabled) => void saveChannelCopy.run(enabled)}
              aria-label="频道副本同步总开关"
            />
            <Text>任务成功后同步副本到用户绑定的频道</Text>
          </Space>
          <Text type="secondary">
            关闭后任务成功不再向用户绑定的频道投递副本（绑定关系保留，重新打开即恢复）；修改即时生效，不影响任务本身。
          </Text>
        </Space>
      </SectionCard>

      <SectionCard title="缓存频道（重复链接复用）">
        <Space direction="vertical" size="small" className="field-width-full">
          <Text>
            当前：
            {data && data.dump_channel_id !== 0 ? (
              <>
                <Tag color="cyan">{data.dump_channel_title || "已配置"}</Tag>
                <Text code>{data.dump_channel_id}</Text>
              </>
            ) : (
              <Tag>未配置（无复用）</Tag>
            )}
          </Text>
          <Space.Compact className="field-width-full">
            <Input
              value={dumpTarget}
              onChange={(e) => setDumpTarget(e.target.value)}
              placeholder="@用户名 / https://t.me/链接 / -100 数字 ID"
              aria-label="缓存频道目标"
            />
            <Button
              type="primary"
              loading={saveDumpChannel.pending}
              disabled={dumpTarget.trim() === "" && data?.dump_channel_id === 0}
              onClick={() => void saveDumpChannel.run(dumpTarget)}
            >
              {dumpTarget.trim() === "" ? "清除配置" : "验证并保存"}
            </Button>
          </Space.Compact>
          <Text type="secondary">
            新建一个私有频道并把机器人设为管理员后，在此填入频道即可启用重复链接复用：同一链接任何人再次提交都直接从缓存频道整条复制，秒级送达（不限媒体大小、相册保组）。公开频道可直接填用户名或链接；私有频道填数字
            ID（频道列表页可复制）。保存的是数字
            ID，频道之后由公开转为私有不影响使用；注意频道<strong>不能开启「限制保存内容」（限制转发）</strong>——Telegram
            会拒绝机器人从该类频道复制消息，复用将失效并自动回落完整下载上传。清空频道配置即关闭。
          </Text>
          <Space>
            <Switch
              checked={data?.tg_reuse_enabled ?? true}
              loading={saveReuse.pending}
              onChange={(enabled) => void saveReuse.run(enabled)}
              aria-label="重复链接复用总开关"
            />
            <Text>重复链接直接复用已投递消息（不重复下载上传）</Text>
          </Space>
          <Text type="secondary">
            关闭后重复链接回到完整下载上传；缓存频道中的副本保留，重新打开即恢复复用。
          </Text>
        </Space>
      </SectionCard>

      <SectionCard title="重复链接检测">
        <Space direction="vertical" size="small" className="field-width-full">
          <Space>
            <InputNumber
              value={dedupMin}
              min={1}
              max={1440}
              precision={0}
              onChange={(v) => setDedupMin(v)}
              aria-label="重复链接检测窗口（分钟）"
            />
            <Button
              loading={saveDedup.pending}
              disabled={!dedupDirty || dedupMin == null || dedupMin < 1 || dedupMin > 1440}
              onClick={() => dedupMin != null && void saveDedup.run(dedupMin)}
            >
              保存窗口
            </Button>
          </Space>
          <Text>重复链接检测窗口（分钟）</Text>
          <Text type="secondary">
            窗口内同一用户重复提交同一链接会被直接拒绝（提示刚处理过）；窗口外若已配置并开启缓存频道复用，则直接复制副本秒回。取值范围为 1–1440 的整数分钟。
          </Text>
        </Space>
      </SectionCard>
      </Space>
    </PageCard>
  );
}
