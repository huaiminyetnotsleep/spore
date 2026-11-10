/**
 * 缓存迁移页（/dump-migrate）：运行历史列表 + 最新运行进度卡。
 * 发起迁移的唯一入口在频道设置页（换频道后立即迁移的上下文）；本页专注
 * 展示与控制——运行中可暂停、已暂停可继续/取消（受控确认，状态即写
 * DB、执行循环批间生效）。运行中 2 秒轮询，空闲停止；逐条明细在
 * /dump-migrate/runs/:id 详情页。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Progress, Space, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { controlDumpMigrate, fetchDumpMigrateRuns, type DumpMigrateControl, type DumpMigrateRun } from "../../api/admin";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { InlineQueryState, SectionQueryState } from "../shared/QueryStates";
import { controlLabels, migrateRunsPollInterval, runCounts, runPercent, runStatusTag } from "./shared";

const { Text } = Typography;

export function DumpMigratePage() {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const navigate = useNavigate();
  const confirm = useConfirmAction();
  // 最新运行即列表首行（id 倒序）；一条查询同时驱动进度卡与历史表
  const runs = useQuery({
    queryKey: ["dumpcache", "runs", page, pageSize],
    queryFn: () => fetchDumpMigrateRuns(page, pageSize),
    refetchInterval: migrateRunsPollInterval,
  });
  const control = useAdminAction({
    action: (action: DumpMigrateControl) => controlDumpMigrate(action),
    invalidate: [["dumpcache", "runs"], ["dumpcache", "migrate"]],
    successText: "迁移状态已更新。",
    onDone: () => void runs.refetch(),
  });

  function runControl(action: DumpMigrateControl, run: DumpMigrateRun) {
    confirm({
      intent: action === "resume" ? "default" : "warning",
      title: `确认${controlLabels[action]}`,
      content: action === "pause"
        ? `运行 #${run.id} 将在当前批次结束后暂停（秒级生效），进度保留，可随时继续。`
        : action === "resume"
          ? `运行 #${run.id} 将从断点继续迁移。`
          : `运行 #${run.id} 将被结束并保留已迁移明细；已复制到目标频道的消息不受影响，之后可重新发起（幂等续跑）。`,
      action: () => control.run(action),
    });
  }

  const latest = runs.data?.items?.[0];
  const active = latest && (latest.status === "running" || latest.status === "paused") ? latest : undefined;

  const columns: ColumnsType<DumpMigrateRun> = [
    { title: "运行", dataIndex: "id", render: (id: number) => <Button type="link" onClick={() => void navigate(`/dump-migrate/runs/${id}`)}>#{id}</Button> },
    { title: "状态", dataIndex: "status", render: runStatusTag },
    { title: "源频道", dataIndex: "from_channel_id" },
    { title: "目标频道", dataIndex: "to", render: (to: number[]) => to.join("、") },
    { title: "进度", render: (_, run) => `${runPercent(run)}%（${runCounts(run)}）` },
    { title: "开始时间", dataIndex: "created_at", render: fmtTime },
    { title: "结束时间", dataIndex: "finished_at", render: (at: number) => (at ? fmtTime(at) : "—") },
    { title: "结束原因", dataIndex: "last_error", render: (text: string) => text || "—" },
  ];
  return (
    <PageScaffold
      title="缓存迁移"
      description="把旧缓存频道仍可读的副本整批复制到当前全部启用的缓存频道；逐条明细与运行历史可回看，支持暂停、继续与取消。"
    >
      <PageSection title="最新运行">
        <SectionQueryState initialLoading={runs.isPending} error={runs.error} hasData={!!runs.data} onRetry={() => void runs.refetch()}>
          {latest ? (
            <Space direction="vertical" size="small" className="field-width-full">
              <InlineQueryState pending={runs.isFetching} text={runs.isFetching ? "正在刷新迁移进度…" : undefined} />
              <div>
                <Text strong>运行 #{latest.id}</Text>
                <Space size="small" wrap className="settings-note">
                  {runStatusTag(latest.status)}
                  <Text type="secondary">源 {latest.from_channel_id} → 目标 {latest.to.join("、")}</Text>
                </Space>
              </div>
              <Progress
                percent={runPercent(latest)}
                status={latest.status === "running" ? "active" : latest.failed > 0 ? "exception" : "normal"}
              />
              <Text>{runCounts(latest)}</Text>
              {latest.last_error ? <Alert type="warning" showIcon message={`结束原因：${latest.last_error}`} /> : null}
              {active ? (
                <ResponsiveActionBar>
                  {active.status === "running"
                    ? <Button loading={control.pending} onClick={() => runControl("pause", active)}>暂停</Button>
                    : <Button type="primary" loading={control.pending} onClick={() => runControl("resume", active)}>继续</Button>}
                  {/* 取消非破坏性（明细保留、可幂等重发）：warning 确认 + 普通按钮，同恢复任务交互 */}
                  <Button loading={control.pending} onClick={() => runControl("cancel", active)}>取消</Button>
                </ResponsiveActionBar>
              ) : null}
              <Button type="link" onClick={() => void navigate(`/dump-migrate/runs/${latest.id}`)}>查看逐条明细</Button>
            </Space>
          ) : (
            <Alert
              type="info"
              showIcon
              message="还没有迁移记录"
              description={<>在 <Link to="/channel-settings">频道设置</Link> 页的「迁移旧缓存」发起；切换或新增缓存频道后建议迁移，免去逐链接重新提取。</>}
            />
          )}
        </SectionQueryState>
      </PageSection>
      <PageSection title="运行历史">
        <InlineQueryState pending={runs.isFetching && !!runs.data} text={runs.isFetching && runs.data ? "正在刷新运行列表…" : undefined} />
        <SectionQueryState initialLoading={runs.isPending} error={runs.error} hasData={!!runs.data} onRetry={() => void runs.refetch()}>
          <DataTable<DumpMigrateRun>
            rowKey="id"
            columns={columns}
            dataSource={runs.data?.items}
            emptyText="暂无迁移运行。"
            pagination={{ current: page, pageSize, total: runs.data?.total ?? 0, onChange: (next, size) => { setPage(next); setPageSize(size); } }}
          />
        </SectionQueryState>
      </PageSection>
    </PageScaffold>
  );
}
