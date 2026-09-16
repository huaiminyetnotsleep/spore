/**
 * 请求详情页（SSR /requests/{id} 的 SPA 对应实现）。
 * 展示来源链接（message_url）、所属用户、状态、尝试次数、错误码与受控
 * 中文文案、媒体诊断元数据与各阶段时间；失败请求提供受控重试
 * （不扣额度，累计尝试 +1；attempt 上限/用户启用由服务端复核）。
 * 云盘请求（delivery_mode=cloud 或有 cloud_uploads 记录）追加「云盘上传」
 * 区块，逐文件列出远端路径/目的地/状态/字节/错误；补存行展示「补存自 #id」
 * 并链接到原请求详情。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Descriptions, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Link, useParams } from "react-router-dom";

import { fetchRequestDetail, fetchSettings, type CloudUploadRow } from "../../api/admin";
import { cancelRequest, dumpBackfillRequest, retryRequest } from "../../api/mutations";
import {
  CLOUD_UPLOAD_STATUS_LABELS,
  CLOUD_UPLOAD_STATUS_TAG_COLORS,
  DELIVERY_MODE_LABELS,
  DELIVERY_MODE_TAG_COLORS,
  REQUEST_STATUS_LABELS,
  REQUEST_STATUS_TAG_COLORS,
  botLabel,
  distKeyText,
  fmtBytes,
  fmtDuration,
  fmtFileSize,
  fmtTime,
  labelOf,
} from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DetailGate, PageCard } from "../shared/PageStates";
import { RequestProgress } from "./RequestProgress";

const { Text } = Typography;

function sourceMediaDCs(ids: number[] | undefined) {
  return ids?.length ? ids.map((id) => `DC ${id}`).join("、") : "—";
}

/** 处理中记录的实时进度轮询间隔（与列表页一致，2 秒）。 */
const PROGRESS_POLL_INTERVAL_MS = 2000;

/** 云盘上传记录表列：文件名/远端路径/目的地/状态/字节/错误码/时间。 */
const cloudUploadColumns: ColumnsType<CloudUploadRow> = [
  {
    title: "文件名",
    dataIndex: "file_name",
    key: "file_name",
    render: (name: string) => name || "—",
  },
  {
    title: "远端路径",
    dataIndex: "remote_path",
    key: "remote_path",
    render: (path: string) => (
      <Text code className="cloud-path-cell">
        {path}
      </Text>
    ),
  },
  { title: "目的地", dataIndex: "destination", key: "destination" },
  {
    title: "状态",
    dataIndex: "status",
    key: "status",
    render: (status: string) => (
      <Tag color={CLOUD_UPLOAD_STATUS_TAG_COLORS[status]}>
        {labelOf(CLOUD_UPLOAD_STATUS_LABELS, status)}
      </Tag>
    ),
  },
  {
    title: "字节",
    dataIndex: "bytes",
    key: "bytes",
    align: "right",
    render: (bytes: number) => fmtBytes(bytes),
  },
  {
    title: "错误码",
    dataIndex: "error_code",
    key: "error_code",
    render: (code: string) => code || "—",
  },
  {
    title: "时间",
    key: "time",
    render: (_, row) => (
      <Text className="cloud-time-cell">
        {fmtTime(row.finished_at || row.created_at)}
      </Text>
    ),
  },
];

export function RequestDetailPage() {
  const { id } = useParams();
  const requestId = id ?? "";
  const requestIdNum = Number(requestId);
  const query = useQuery({
    queryKey: ["requests", "detail", requestId],
    queryFn: () => fetchRequestDetail(requestId),
    enabled: Boolean(requestId),
    // 处理中记录轮询实时进度（内存态字段，2 秒刷新足够）；其余状态不轮询
    refetchInterval: (q) =>
      q.state.data?.status === "processing" ? PROGRESS_POLL_INTERVAL_MS : false,
    refetchIntervalInBackground: false,
  });
  const { data: detail } = query;

  const confirm = useConfirmAction();
  const retry = useAdminAction({
    action: (targetId: number) => retryRequest(targetId),
    invalidate: [["requests"], ["overview"]],
    successText: "已重新入队（不扣减额度）",
  });
  const cancel = useAdminAction({
    action: (targetId: number) => cancelRequest(targetId),
    invalidate: [["requests"], ["channels"], ["overview"]],
    successText: "请求已取消。",
  });
  // 缓存补写：终态记录可转存缓存频道（已有副本/未配置由服务端复核，
  // 拒绝时展示服务端受控文案，不误报成功）。
  const dumpBackfill = useAdminAction({
    action: (targetId: number) => dumpBackfillRequest(targetId),
    invalidate: [["requests"], ["overview"]],
    successText: "已创建缓存补写任务，完成后副本写入缓存频道（不打扰用户）。",
  });
  // 缓存频道配置只用于入口可用性判断；查询失败不阻塞详情展示。
  const settings = useQuery({
    queryKey: ["settings"],
    queryFn: fetchSettings,
    staleTime: 30_000,
  });
  const dumpChannelReady = (settings.data?.dump_channel_id ?? 0) !== 0;

  // 可重试入口与 SSR 一致：仅 failed 且未达尝试上限（用户启用由服务端复核，
  // 拒绝时展示服务端受控文案，不误报成功）。
  const canRetry =
    detail !== undefined &&
    detail.status === "failed" &&
    detail.attempt < detail.attempt_max;
  const canCancel =
    detail !== undefined &&
    (detail.status === "queued" || detail.status === "processing");
  const canDumpBackfill =
    detail !== undefined &&
    ["succeeded", "failed", "cancelled"].includes(detail.status);

  return (
    <Space direction="vertical" size="middle" className="field-width-full">
      <DetailGate
        loading={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
        notFoundTitle="请求不存在"
        backTo="/requests"
        backText="返回请求记录"
      >
        {detail && (
          <PageCard
            title={`请求 ${detail.id}`}
            extra={
              <Link to="/requests">
                <Button>返回请求记录</Button>
              </Link>
            }
          >
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label="来源链接">
                {detail.message_url ? (
                  <>
                    <a href={detail.message_url} target="_blank" rel="noopener noreferrer">
                      {detail.channel_link_text}
                    </a>
                    {detail.source_kind === "private" ? (
                      <Tag color="gold" className="layout-margin-inline-start-8">
                        私有，需权限
                      </Tag>
                    ) : null}
                  </>
                ) : (
                  <Text type="secondary">链接不可用</Text>
                )}
              </Descriptions.Item>
              <Descriptions.Item label="所属用户">
                <Link to={`/users/${detail.user_id}`}>{detail.user_id}</Link>
                {detail.username ? `（@${detail.username}）` : ""}
              </Descriptions.Item>
              <Descriptions.Item label="所属频道">
                <Link to={`/channels/${encodeURIComponent(detail.channel_key)}`}>
                  {detail.channel_key}
                </Link>
              </Descriptions.Item>
              <Descriptions.Item label="状态">
                <Tag color={REQUEST_STATUS_TAG_COLORS[detail.status]}>
                  {labelOf(REQUEST_STATUS_LABELS, detail.status)}
                </Tag>
              </Descriptions.Item>
              <Descriptions.Item label="尝试次数">
                {detail.attempt} / {detail.attempt_max}（累计含首次）
              </Descriptions.Item>
              {detail.error_code ? (
                <Descriptions.Item label="错误">
                  {detail.error_code}
                  <Text type="secondary">（{detail.error_text}）</Text>
                </Descriptions.Item>
              ) : null}
              <Descriptions.Item label="媒体类型">
                {distKeyText(detail.media_type)}
              </Descriptions.Item>
              {detail.media_type === "album" ? (
                <Descriptions.Item label="相册内容">
                  {detail.media_types?.length ? detail.media_types.join(" + ") : "—"}
                </Descriptions.Item>
              ) : null}
              <Descriptions.Item label="大小 / 文件名">
                {fmtFileSize(detail.file_size)} / {detail.file_name || "—"}
              </Descriptions.Item>
              <Descriptions.Item label="源媒体数据中心">
                <Text>{sourceMediaDCs(detail.source_media_dc_ids)}</Text>
              </Descriptions.Item>
              <Descriptions.Item label="投递方式">
                <Tag color={DELIVERY_MODE_TAG_COLORS[detail.delivery_mode]}>
                  {labelOf(DELIVERY_MODE_LABELS, detail.delivery_mode)}
                </Tag>
              </Descriptions.Item>
              <Descriptions.Item label="受理机器人">
                {botLabel(detail.bot_id, detail.bot_username)}
              </Descriptions.Item>
              {detail.parent_request_id ? (
                <Descriptions.Item label="补存来源">
                  <Link to={`/requests/${detail.parent_request_id}`}>
                    补存自 #{detail.parent_request_id}
                  </Link>
                  <Text type="secondary" className="layout-margin-inline-start-8">
                    （管理端「存到网盘」按原链接重抓取创建的本请求）
                  </Text>
                </Descriptions.Item>
              ) : null}
              {detail.progress ? (
                <Descriptions.Item label="实时进度">
                  <RequestProgress progress={detail.progress} />
                </Descriptions.Item>
              ) : null}
              <Descriptions.Item label="请求 / 入队">
                {fmtTime(detail.requested_at)} / {fmtTime(detail.queued_at)}
              </Descriptions.Item>
              <Descriptions.Item label="开始 / 完成">
                {fmtTime(detail.started_at)} / {fmtTime(detail.finished_at)}
              </Descriptions.Item>
              <Descriptions.Item label="耗时">{fmtDuration(detail.duration_ms)}</Descriptions.Item>
            </Descriptions>
            {detail.delivery_mode === "cloud" || (detail.cloud_uploads?.length ?? 0) > 0 ? (
              <Table<CloudUploadRow>
                className="cloud-uploads-table"
                title={() => (
                  <Text strong>
                    云盘上传（{detail.cloud_uploads?.length ?? 0} 个文件）
                  </Text>
                )}
                rowKey={(row) => `${row.destination}:${row.remote_path}:${row.created_at}`}
                size="small"
                columns={cloudUploadColumns}
                dataSource={detail.cloud_uploads ?? []}
                pagination={false}
                locale={{ emptyText: "尚无上传记录（任务排队或未开始上传）。" }}
              />
            ) : null}
            {canCancel ? (
              <Alert
                type="warning"
                showIcon
                message="该请求仍在执行，可以取消"
                description={
                  <Space direction="vertical">
                    <Text type="secondary">取消后不可恢复，但会保留请求记录，不会删除统计事实。</Text>
                    <Button
                      size="small"
                      danger
                      loading={cancel.pending}
                      disabled={cancel.pending}
                      onClick={() =>
                        confirm("确定取消该请求？取消后不可恢复，但不会删除记录。", () => {
                          void cancel.run(requestIdNum);
                        })
                      }
                    >
                      取消请求
                    </Button>
                  </Space>
                }
              />
            ) : null}
            {canDumpBackfill ? (
              <Alert
                type="info"
                showIcon
                message="可将该记录转存缓存频道"
                description={
                  <Space direction="vertical">
                    <Text type="secondary">
                      {dumpChannelReady
                        ? "按原链接重新获取源消息并写入缓存频道干净副本（不带用户脚注），全程不向用户发送任何消息；缓存频道已有该链接副本时会被跳过。"
                        : "缓存频道未配置，可先在「运行设置」页配置缓存频道。"}
                    </Text>
                    <Button
                      size="small"
                      loading={dumpBackfill.pending}
                      disabled={dumpBackfill.pending || !dumpChannelReady}
                      onClick={() =>
                        confirm(
                          "确定转存缓存频道？将重新获取源消息，不向用户发送任何消息。",
                          () => {
                            void dumpBackfill.run(requestIdNum);
                          },
                        )
                      }
                    >
                      转存缓存频道
                    </Button>
                  </Space>
                }
              />
            ) : null}
            {canRetry ? (
              <Alert
                type="warning"
                showIcon
                message="该请求失败，可受控重试"
                description={
                  <Space direction="vertical">
                    <Text type="secondary">
                      重试不扣减额度，累计尝试 +1；所属用户停用或队列饱和时会被拒绝。
                    </Text>
                    <Button
                      size="small"
                      type="primary"
                      loading={retry.pending}
                      disabled={retry.pending}
                      onClick={() =>
                        confirm("确定重试该请求？不扣减额度，累计尝试 +1。", () => {
                          void retry.run(requestIdNum);
                        })
                      }
                    >
                      重试
                    </Button>
                  </Space>
                }
              />
            ) : (
              <Text type="secondary">
                {detail.status === "failed" ? "已达最大尝试次数，无法重试。" : "仅失败请求可重试。"}
              </Text>
            )}
          </PageCard>
        )}
      </DetailGate>
    </Space>
  );
}
