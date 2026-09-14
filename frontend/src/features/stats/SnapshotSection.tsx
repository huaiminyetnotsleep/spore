/**
 * 全时段快照区（业务统计页底部）：用户与频道加入两组计数的当前快照。
 * 数据来自 GET /api/v1/overview（与总览页共享 ["overview"] 查询缓存），
 * 全时段口径不受页面上方时间筛选影响；查询失败只影响本区，不拖垮统计区。
 * 版式：宽屏双面板并排（snapshot-grid 自适应换行），面板头 = 色点 + 标题 +
 * 汇总数值（用户总数 / 已加入额度），面板体 = 按语义合并的 MetricGrid 卡
 * （用户状态一卡，加入审批与当前加入两卡），避免逐状态小卡过多。
 */
import { useQuery } from "@tanstack/react-query";
import { Spin, Typography } from "antd";

import { fetchOverview } from "../../api/admin";
import { LoadError, SectionCard } from "../shared/PageStates";
import { MetricGrid } from "../shared/MetricGrid";

const { Text } = Typography;

export function SnapshotSection() {
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["overview"],
    queryFn: () => fetchOverview(),
  });

  if (isPending) {
    return (
      <SectionCard title="全时段快照" extra={<Text type="secondary">不受时间筛选影响</Text>}>
        <Spin className="section-loading" />
      </SectionCard>
    );
  }
  if (isError || !data) {
    return (
      <SectionCard title="全时段快照" extra={<Text type="secondary">不受时间筛选影响</Text>}>
        <LoadError onRetry={() => void refetch()} />
      </SectionCard>
    );
  }

  const { users, join } = data;

  return (
    <SectionCard title="全时段快照" extra={<Text type="secondary">不受时间筛选影响</Text>}>
      <div className="snapshot-grid">
        <div className="snapshot-panel">
          <div className="snapshot-panel__head">
            <span className="snapshot-panel__dot" />
            用户
            <span className="snapshot-panel__meta">总数 {users.total}</span>
          </div>
          <MetricGrid
            items={[
              {
                label: "用户状态",
                stats: [
                  { label: "已启用", value: users.enabled, tone: "success" },
                  { label: "待审批", value: users.pending, tone: "warning" },
                  { label: "已禁用", value: users.disabled, tone: "danger" },
                  { label: "已归档", value: users.archived },
                ],
              },
            ]}
          />
        </div>
        <div className="snapshot-panel">
          <div className="snapshot-panel__head">
            <span className="snapshot-panel__dot snapshot-panel__dot--green" />
            频道加入
            <span className="snapshot-panel__meta">
              已加入 {join.active_joined}
              {join.max_channels > 0 ? ` / ${join.max_channels}` : ""}
            </span>
          </div>
          <MetricGrid
            maxColumns={2}
            items={[
              {
                label: "加入审批",
                stats: [
                  { label: "待审批", value: join.pending, tone: "warning" },
                  { label: "已通过", value: join.approved, tone: "success" },
                  { label: "已拒绝", value: join.rejected },
                  { label: "加入失败", value: join.failed, tone: "danger" },
                ],
              },
              {
                label: "当前加入",
                stats: [
                  { label: "外部拉入", value: join.external_active, tone: "warning" },
                  { label: "已退出", value: join.left_total },
                ],
              },
            ]}
          />
        </div>
      </div>
    </SectionCard>
  );
}
