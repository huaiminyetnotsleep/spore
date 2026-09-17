/**
 * 请求实时传输进度（列表与详情共用）：下载/上传两个独立圆形进度条。
 * 下载与上传是重叠进行的两条管线（边下边传），分别展示百分比与字节数。
 * 仅 processing 状态且服务端下发 progress 时渲染，其余场景显示 "—"。
 */
import { Progress, Space, Typography } from "antd";

import type { RequestRow } from "../../api/admin";
import { fmtBytes } from "../../shared/format";

const { Text } = Typography;

export type RequestProgressData = NonNullable<RequestRow["progress"]>;

/** 总量未知（尚未登记媒体大小）或异常溢出时按 0% 展示，封顶 100%。 */
function percentOf(bytes: number, total: number): number {
  if (total <= 0 || bytes <= 0) return 0;
  return Math.min(100, Math.round((bytes / total) * 100));
}

export function RequestProgress({ progress }: { progress: RequestProgressData }) {
  const dlPercent = percentOf(progress.downloaded_bytes, progress.total_bytes);
  const upPercent = percentOf(progress.uploaded_bytes, progress.total_bytes);
  return (
    <Space size={12}>
      <div className="request-progress-item">
        <Progress
          type="circle"
          size={44}
          strokeWidth={8}
          percent={dlPercent}
          format={(p) => `${p ?? 0}%`}
        />
        <Text type="secondary">下载 {fmtBytes(progress.downloaded_bytes)}</Text>
      </div>
      <div className="request-progress-item">
        <Progress
          type="circle"
          size={44}
          strokeWidth={8}
          percent={upPercent}
          format={(p) => `${p ?? 0}%`}
        />
        <Text type="secondary">上传 {fmtBytes(progress.uploaded_bytes)}</Text>
      </div>
    </Space>
  );
}

/** 无进度数据时的占位（终态/排队/文本请求）。 */
export function RequestProgressEmpty() {
  return <Text type="secondary">—</Text>;
}
