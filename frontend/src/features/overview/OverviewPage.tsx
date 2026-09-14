/**
 * 总览页（/）：职责 = 系统现在怎么样。第一屏「状态一览」四张彩色状态卡
 * （MTProto 会话 / 数据库 / 队列水位 / 待办提醒），下方紧凑「服务信息」卡。
 * 用户计数与频道加入快照已迁至 /stats 的「全时段快照」区。
 */
import { useQuery } from "@tanstack/react-query";
import { Card, Descriptions, Space, Spin, Tag, Typography } from "antd";
import { lazy, Suspense, type ReactNode } from "react";
import { Link } from "react-router-dom";

import { fetchOverview } from "../../api/admin";
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
          <Descriptions.Item label="服务版本">{data.version}</Descriptions.Item>
          <Descriptions.Item label="启动时间">{fmtTime(data.started_at)}</Descriptions.Item>
          <Descriptions.Item label="监听地址">{data.addr}</Descriptions.Item>
          <Descriptions.Item label="Worker 数">{data.workers}</Descriptions.Item>
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
