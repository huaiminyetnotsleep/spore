/**
 * 迁移运行详情页（/dump-migrate/runs/:id）：运行元信息 + 逐条明细表
 * （状态筛选、分页；来源坐标 → 各目标频道新消息 ID / 失败原因）。
 * 运行中 2 秒轮询，空闲停止。控制操作在 /dump-migrate 列表页（作用于
 * 当前活跃运行）。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Descriptions, Progress, Select, Space, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { fetchDumpMigrateItems, fetchDumpMigrateRun, type DumpMigrateItem, type DumpMigrateItemStatus } from "../../api/admin";
import { fmtTime } from "../../shared/format";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { DetailGate, DetailNotFound } from "../shared/PageStates";
import { InlineQueryState, SectionQueryState } from "../shared/QueryStates";
import { itemLabels, itemResults, itemStatusTag, migrateRunPollInterval, runCounts, runPercent, runStatusTag } from "./shared";

const { Text } = Typography;

export function DumpMigrateRunDetailPage() {
  const params = useParams();
  const id = Number(params.id);
  const valid = Number.isInteger(id) && id > 0 ? id : undefined;
  const navigate = useNavigate();
  const [itemPage, setItemPage] = useState(1);
  const [itemPageSize, setItemPageSize] = useState(20);
  const [itemStatus, setItemStatus] = useState<DumpMigrateItemStatus>();
  const run = useQuery({
    queryKey: ["dumpcache", "run", id],
    queryFn: () => fetchDumpMigrateRun(id!),
    enabled: valid !== undefined,
    refetchInterval: migrateRunPollInterval,
  });
  const items = useQuery({
    queryKey: ["dumpcache", "run-items", id, itemPage, itemPageSize, itemStatus],
    queryFn: () => fetchDumpMigrateItems(id!, itemPage, itemPageSize, itemStatus),
    enabled: valid !== undefined,
    refetchInterval: () => migrateRunPollInterval({ state: { data: run.data } }),
  });

  const columns: ColumnsType<DumpMigrateItem> = [
    { title: "来源频道", dataIndex: "channel_key" },
    { title: "消息 ID", dataIndex: "message_id" },
    { title: "状态", dataIndex: "status", render: itemStatusTag },
    { title: "目标频道落地（新消息 ID / 错误）", render: (_, item) => itemResults(item) },
    { title: "失败摘要", dataIndex: "error", render: (text: string) => text || "—" },
    { title: "处理时间", dataIndex: "created_at", render: fmtTime },
  ];
  if (valid === undefined) {
    return (
      <PageScaffold title="迁移运行详情" description="单次缓存迁移的运行元信息与逐条明细。">
        <DetailNotFound title="迁移运行不存在" backTo="/dump-migrate" backText="返回缓存迁移" />
      </PageScaffold>
    );
  }
  return (
    <PageScaffold
      title="迁移运行详情"
      description="单次缓存迁移的运行元信息与逐条明细；来源坐标右侧为各目标频道的落地消息 ID。"
      actions={<Button onClick={() => void navigate("/dump-migrate")}>返回缓存迁移</Button>}
    >
      <DetailGate loading={run.isPending} error={run.error} onRetry={() => void run.refetch()} notFoundTitle="迁移运行不存在" backTo="/dump-migrate" backText="返回缓存迁移">
        {run.data ? (
          <>
            <PageSection title={`运行 #${run.data.id}`}>
              <Space direction="vertical" size="small" className="field-width-full">
                <InlineQueryState pending={run.isFetching} text={run.isFetching ? "正在刷新运行详情…" : undefined} />
                <Descriptions column={1} items={[
                  { key: "status", label: "状态", children: runStatusTag(run.data.status) },
                  { key: "route", label: "源 → 目标", children: `${run.data.from_channel_id} → ${run.data.to.join("、")}` },
                  { key: "progress", label: "进度", children: <span className="settings-note">{runCounts(run.data)}（{runPercent(run.data)}%）</span> },
                  { key: "created", label: "开始时间", children: fmtTime(run.data.created_at) },
                  { key: "finished", label: "结束时间", children: run.data.finished_at ? fmtTime(run.data.finished_at) : "—" },
                  { key: "error", label: "结束原因", children: run.data.last_error || "—" },
                ]} />
                <Progress percent={runPercent(run.data)} status={run.data.status === "running" ? "active" : run.data.failed > 0 ? "exception" : "normal"} />
              </Space>
            </PageSection>
            <PageSection title="逐条明细" extra={
              <Select
                allowClear
                placeholder="全部状态"
                className="filter-select"
                virtual={false}
                value={itemStatus}
                onChange={(value) => { setItemStatus(value); setItemPage(1); }}
                options={(Object.keys(itemLabels) as DumpMigrateItemStatus[]).map((status) => ({ value: status, label: itemLabels[status] }))}
                aria-label="明细状态筛选"
              />
            }>
              <SectionQueryState initialLoading={items.isPending} error={items.error} hasData={!!items.data} onRetry={() => void items.refetch()}>
                <DataTable<DumpMigrateItem>
                  density="compact"
                  rowKey="id"
                  columns={columns}
                  dataSource={items.data?.items}
                  emptyText="暂无明细（运行进行中逐条落库，或按当前筛选无记录）。"
                  pagination={{ current: itemPage, pageSize: itemPageSize, total: items.data?.total ?? 0, onChange: (next, size) => { setItemPage(next); setItemPageSize(size); } }}
                />
              </SectionQueryState>
            </PageSection>
          </>
        ) : (
          <Text type="secondary">运行数据不可用。</Text>
        )}
      </DetailGate>
    </PageScaffold>
  );
}
