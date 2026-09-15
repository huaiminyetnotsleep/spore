/**
 * 总览页（/）：职责 = 系统现在怎么样。第一屏「状态一览」四张彩色状态卡
 * （MTProto 会话 / 数据库 / 队列水位 / 待办提醒），下方紧凑「服务信息」卡。
 * 服务版本支持手动检查更新（点击刷新按钮查询上游最新发布，落后时给出
 * 升级提示图标）。用户计数与频道加入快照已迁至 /stats 的「全时段快照」区。
 */
import { useMutation, useQuery } from "@tanstack/react-query";
import { Button, Card, Descriptions, Space, Spin, Tag, Typography } from "antd";
import { ArrowUpOutlined, ReloadOutlined } from "@ant-design/icons";
import { lazy, Suspense, type ReactNode } from "react";
import { Link } from "react-router-dom";

import { fetchOverview, fetchVersionCheck, type OverviewBot } from "../../api/admin";
import {
  MTPROTO_STATE_LABELS,
  botAPIStateText,
  fmtBytes,
  fmtTime,
  labelOf,
} from "../../shared/format";
import { LoadError, PageCard, SectionCard } from "../shared/PageStates";

const SystemMetricsSection = lazy(() => import("./SystemMetricsSection").then((module) => ({ default: module.SystemMetricsSection })));
const { Text } = Typography;

/** 状态卡色调：绿=正常，橙=需要注意，红=异常。 */
type TileTone = "ok" | "warn" | "error";

function mtprotoTone(state: string): TileTone {
  if (state === "ready") return "ok";
  if (state === "login_pending") return "warn";
  return "error";
}

function mtprotoTagColor(state: string): string {
  if (state === "ready") return "green";
  if (state === "login_pending") return "gold";
  return "red";
}

/** Bot MTProto 会话展示文案：状态 + 当前主 DC（DC 表示数据中心，非地理位置）。 */
function botSessionText(state: string | undefined, dcID: number | undefined): string {
  if (!state) return "未接入";
  if (state !== "ready") return "离线";
  return dcID && dcID > 0 ? `已连接 · DC ${dcID}` : "已连接 · DC 未知";
}

/** 接入机器人展示文案：Name（@username）；未就绪/未接入时显示"未接入"。 */
function botIdentityText(bot: OverviewBot | undefined): string {
  if (!bot) return "未接入";
  return bot.username ? `${bot.name}（@${bot.username}）` : bot.name;
}

/** 服务版本展示：CI 在 main/PR 构建注入完整 commit SHA，太长难读——
 * 仅在展示层截取为 7 位短哈希（悬停 title 可见完整值）；版本值本身
 * 不动，tag 版本号与 dev 等其余取值原样展示。 */
function displayVersion(v: string): string {
  return /^[0-9a-f]{40}$/i.test(v) ? v.slice(0, 7) : v;
}

/** 版本检查结果提示：落后 → 橙色升级标签（链接到发布页）；最新 → 绿色；
 * 无法比较（dev 构建）→ 展示上游最新版；查询失败 → 红色受控文案。 */
function VersionCheckHint({
  status,
  latestVersion,
  releaseUrl,
  error,
}: {
  status: string | undefined;
  latestVersion: string | undefined;
  releaseUrl: string | undefined;
  error: string | null;
}) {
  if (error) {
    return (
      <Tag color="red" data-testid="version-check-error">
        {error}
      </Tag>
    );
  }
  if (status === "outdated") {
    const label = latestVersion ? `可升级 ${latestVersion}` : "可升级";
    return (
      <Tag color="orange" icon={<ArrowUpOutlined />} data-testid="version-upgrade-hint">
        {releaseUrl ? (
          <a href={releaseUrl} target="_blank" rel="noreferrer">
            {label}
          </a>
        ) : (
          label
        )}
      </Tag>
    );
  }
  if (status === "up_to_date") {
    return (
      <Tag color="green" data-testid="version-up-to-date">
        已是最新
      </Tag>
    );
  }
  if (status === "unknown" && latestVersion) {
    return <Tag>最新 {latestVersion}</Tag>;
  }
  return null;
}

function StatusTile({
  tone,
  testId,
  label,
  children,
}: {
  tone: TileTone;
  testId: string;
  label: string;
  children: ReactNode;
}) {
  return (
    <Card className={`status-tile status-tile--${tone}`} data-testid={testId}>
      <Text type="secondary">{label}</Text>
      <div className="status-tile__body">{children}</div>
    </Card>
  );
}

export function OverviewPage() {
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["overview"],
    queryFn: () => fetchOverview(),
  });
  // 检查更新：进入页面自动查询一次（服务端有 1h 缓存窗口，频度无虞）；
  // 管理员点刷新按钮时强制绕过缓存重查（点了就要最新结果）
  const versionCheck = useQuery({
    queryKey: ["version-check"],
    queryFn: () => fetchVersionCheck(false),
    retry: false,
  });
  const forceCheck = useMutation({ mutationFn: () => fetchVersionCheck(true) });

  if (isPending) {
    return (
      <SectionCard data-testid="spa-shell">
        <Spin className="page-loading" tip="加载中…" />
      </SectionCard>
    );
  }
  if (isError || !data) {
    return <LoadError onRetry={() => void refetch()} />;
  }

  const health = data.health;
  const backlog = data.requests.queued_rows + data.requests.processing_rows;
  const todoCount = data.join.pending + data.users.pending;

  return (
    <Space direction="vertical" size="middle" className="field-width-full overview-page" data-testid="spa-shell">
      <PageCard title="实时运行状态">
        <div className="status-tiles">
        <StatusTile tone={mtprotoTone(health.mtproto_state)} testId="status-tile-mtproto" label="MTProto 会话">
          <Space wrap size={8}>
            <Tag color={mtprotoTagColor(health.mtproto_state)}>
              {labelOf(MTPROTO_STATE_LABELS, health.mtproto_state)}
            </Tag>
            <Link to="/mtproto">管理</Link>
          </Space>
          {health.mtproto_error ? (
            <Text type="secondary" className="status-tile__meta">
              {health.mtproto_error}
            </Text>
          ) : null}
        </StatusTile>

        <StatusTile
          tone={health.store_ok ? "ok" : "error"}
          testId="status-tile-store"
          label="数据库"
        >
          <div className="status-tile__value">
            <Tag color={health.store_ok ? "green" : "red"}>{health.store_ok ? "正常" : "不可用"}</Tag>
          </div>
          <Text type="secondary" className="status-tile__meta">
            占用 {fmtBytes(health.db_size_bytes)}
          </Text>
        </StatusTile>

        <StatusTile
          tone={backlog > 0 ? "warn" : "ok"}
          testId="status-tile-queue"
          label="队列水位"
        >
          <div className="status-tile__value">
            {data.queue ? `${data.queue.len} / ${data.queue.cap}` : "—"}
          </div>
          <Text type="secondary" className="status-tile__meta">
            排队中 {data.requests.queued_rows} · 处理中 {data.requests.processing_rows}（记录）
          </Text>
        </StatusTile>

        <StatusTile
          tone={todoCount > 0 ? "warn" : "ok"}
          testId="status-tile-todo"
          label="待办"
        >
          <div className="status-tile__value status-tile__value--compact">
            <Link to="/invite-approvals">待审批加入 {data.join.pending}</Link>
          </div>
          <div className="status-tile__value status-tile__value--compact">
            <Link to="/users">待审批用户 {data.users.pending}</Link>
          </div>
          <Text type="secondary" className="status-tile__meta">
            点击前往处理
          </Text>
        </StatusTile>
        </div>
      </PageCard>

      <PageCard title="服务信息">
        <Descriptions column={{ xs: 1, md: 2 }} size="small" bordered>
          <Descriptions.Item label="服务版本">
            <Space size={6} wrap>
              <span title={data.version}>{displayVersion(data.version)}</span>
              <Button
                size="small"
                type="text"
                aria-label="检查更新"
                title="检查更新（跳过缓存）"
                icon={<ReloadOutlined />}
                loading={forceCheck.isPending}
                onClick={() => forceCheck.mutate()}
              />
              {/* 手动刷新结果优先于进入页面的自动检查 */}
              <VersionCheckHint
                status={forceCheck.data?.status ?? versionCheck.data?.status}
                latestVersion={forceCheck.data?.latest_version ?? versionCheck.data?.latest_version}
                releaseUrl={forceCheck.data?.release_url ?? versionCheck.data?.release_url}
                error={forceCheck.error?.message ?? versionCheck.error?.message ?? null}
              />
            </Space>
          </Descriptions.Item>
          <Descriptions.Item label="启动时间">{fmtTime(data.started_at)}</Descriptions.Item>
          <Descriptions.Item label="监听地址">{data.addr}</Descriptions.Item>
          <Descriptions.Item label="Worker 数">{data.workers}</Descriptions.Item>
          <Descriptions.Item label="机器人">{botIdentityText(data.bot)}</Descriptions.Item>
          <Descriptions.Item label="Bot API 长轮询">
            {botAPIStateText(health.mtproto_state)}
            <Text type="secondary">（随 MTProto 会话启停）</Text>
          </Descriptions.Item>
          <Descriptions.Item label="Bot MTProto 会话">
            {botSessionText(health.bot_mtproto_state, health.bot_mtproto_dc_id)}
          </Descriptions.Item>
          <Descriptions.Item label="GitHub 登录通道">
            {health.github_configured ? "已配置" : "未配置（仅密钥登录）"}
          </Descriptions.Item>
          <Descriptions.Item label="数据库路径">
            <span className="source-link">{health.db_path}</span>
          </Descriptions.Item>
          <Descriptions.Item label="临时目录">
            {fmtBytes(health.temp_dir_bytes)}
            <Text type="secondary">（{health.temp_dir}）</Text>
          </Descriptions.Item>
        </Descriptions>
      </PageCard>

      <Suspense fallback={<SectionCard loading />}>
        <SystemMetricsSection />
      </Suspense>
    </Space>
  );
}
