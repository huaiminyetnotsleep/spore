/**
 * 请求详情页（SSR /requests/{id} 的 SPA 对应实现）。
 * 展示来源链接（message_url）、所属用户、状态、尝试次数、错误码与受控
 * 中文文案、媒体诊断元数据与各阶段时间；失败请求提供受控重试
 * （不扣额度，累计尝试 +1；attempt 上限/用户启用由服务端复核）。
 * 已达尝试上限的失败请求提供「重置尝试计数」（attempt 清回 1、不入队，
 * 清零后再点重试即可放行）。云盘请求（delivery_mode=cloud 或有
 * cloud_uploads 记录）追加「云盘上传」区块，逐文件列出远端路径/目的地/
 * 状态/字节/错误；补存行展示「补存自 #id」并链接到原请求详情。取消/转存/
 * 重试/重置集中在「详情操作」分区，确认意图按共享契约分级
 * （取消/重置=warning，转存/重试=default），pending 期间防重复提交。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Descriptions, Space, Tag, Tooltip, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Link, useNavigate, useParams } from "react-router-dom";

import { fetchRequestDetail, fetchSettings, type CloudUploadRow } from "../../api/admin";
import {
  cancelRequest,
  dumpBackfillRequest,
  resetRequestAttempts,
  retryRequest,
} from "../../api/mutations";
import {
  CLOUD_UPLOAD_STATUS_LABELS,
  CLOUD_UPLOAD_STATUS_TAG_COLORS,
  DELIVERY_MODE_LABELS,
  DELIVERY_MODE_TAG_COLORS,
  REQUEST_STATUS_LABELS,
  REQUEST_STATUS_TAG_COLORS,
  botLabel,
  distKeyText,
  errorCodeLabel,
  fmtBytes,
  fmtDuration,
  fmtFileSize,
  fmtTime,
  labelOf,
} from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { DetailGate } from "../shared/PageStates";
import { RequestProgress } from "./RequestProgress";

const { Text, Paragraph } = Typography;

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
  const navigate = useNavigate();
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
  // 重置尝试计数：仅 failed 且已达上限的行展示；attempt 清回 1、不入队，
  // 清零后由管理员显式重试。
  const resetAttempts = useAdminAction({
    action: (targetId: number) => resetRequestAttempts(targetId),
    invalidate: [["requests"]],
    successText: "尝试计数已重置为 1；需要重新执行请再点「重试」。",
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
  // 已达上限的失败请求：重试入口被关闭，展示重置计数入口定向放行
  const canResetAttempts =
    detail !== undefined &&
    detail.status === "failed" &&
    detail.attempt >= detail.attempt_max;
  const canCancel =
    detail !== undefined &&
    (detail.status === "queued" || detail.status === "processing");
  const canDumpBackfill =
    detail !== undefined &&
    ["succeeded", "failed", "cancelled"].includes(detail.status);

  return (
    <PageScaffold
      title="请求详情"
      description="展示来源、状态、尝试次数与各阶段时间；失败请求可受控重试。"
      actions={<Button onClick={() => void navigate("/requests")}>返回请求记录</Button>}
    >
      <DetailGate
        loading={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
        notFoundTitle="请求不存在"
        backTo="/requests"
        backText="返回请求记录"
      >
        {detail && (
          <>
            <PageSection title={`请求 ${detail.id}`}>
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
                    <Tag color="red">{errorCodeLabel(detail.error_code)}</Tag>
                    <Text type="secondary">（{detail.error_code}：{detail.error_text}）</Text>
                    {detail.error_detail ? (
                      <Paragraph type="secondary" className="layout-margin-bottom-0">
                        根因：{detail.error_detail}
                      </Paragraph>
                    ) : null}
                    <Link to={`/error-logs?request_id=${detail.id}`}>查看相关日志</Link>
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
                {detail.pin ? (
                  <Descriptions.Item label="自动置顶">
                    {detail.status === "succeeded" ? (
                      detail.pin_total > 0 ? (
                        <Text>
                          📌 已置顶 {detail.pin_ok}/{detail.pin_total} 个目标
                        </Text>
                      ) : (
                        <Text type="secondary">📌 无置顶结果（无媒体副本或完成时未绑定）</Text>
                      )
                    ) : (
                      <Text type="secondary">📌 完成后自动置顶到绑定频道/群组</Text>
                    )}
                  </Descriptions.Item>
                ) : null}
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
            </PageSection>

            {detail.delivery_mode === "cloud" || (detail.cloud_uploads?.length ?? 0) > 0 ? (
              <PageSection title={`云盘上传（${detail.cloud_uploads?.length ?? 0} 个文件）`}>
                <DataTable<CloudUploadRow>
                  density="compact"
                  rowKey={(row) => `${row.destination}:${row.remote_path}:${row.created_at}`}
                  columns={cloudUploadColumns}
                  dataSource={detail.cloud_uploads ?? []}
                  pagination={false}
                  emptyText="尚无上传记录（任务排队或未开始上传）。"
                />
              </PageSection>
            ) : null}

            <PageSection title="详情操作">
              {/* 详情页空间充足：操作平铺为正常尺寸按钮（不用「更多」菜单、不用
                  文字按钮）——重试为 primary 主操作，取消为 danger，转存为普通
                  按钮；窄屏由 ResponsiveActionBar 换行。确认意图、pending 与
                  禁用语义不变。 */}
              <Space direction="vertical" size="middle" className="field-width-full">
                {canCancel ? (
                  <Alert
                    type="warning"
                    showIcon
                    message="该请求仍在执行，可以取消"
                    description={
                      <Text type="secondary">
                        取消后不可恢复，但会保留请求记录，不会删除统计事实。
                      </Text>
                    }
                  />
                ) : null}
                {canDumpBackfill ? (
                  <Alert
                    type="info"
                    showIcon
                    message="可将该记录转存缓存频道"
                    description={
                      <Text type="secondary">
                        {dumpChannelReady
                          ? "按原链接重新获取源消息并写入缓存频道干净副本（不带用户脚注），全程不向用户发送任何消息；缓存频道已有该链接副本时会被跳过。"
                          : "缓存频道未配置，可先在「运行设置」页配置缓存频道。"}
                      </Text>
                    }
                  />
                ) : null}
                {canRetry ? (
                  <Alert
                    type="warning"
                    showIcon
                    message="该请求失败，可受控重试"
                    description={
                      <Text type="secondary">
                        重试不扣减额度，累计尝试 +1；所属用户停用或队列饱和时会被拒绝。
                      </Text>
                    }
                  />
                ) : canResetAttempts ? (
                  <Text type="secondary">
                    已达最大尝试次数，无法重试；可重置尝试计数后再次重试。
                  </Text>
                ) : (
                  <Text type="secondary">仅失败请求可重试。</Text>
                )}
                <ResponsiveActionBar align="start">
                  {canRetry ? (
                    <Button
                      type="primary"
                      loading={retry.pending}
                      disabled={retry.pending}
                      onClick={() =>
                        confirm({
                          intent: "default",
                          title: "确认重试请求",
                          content: "确定重试该请求？不扣减额度，累计尝试 +1。",
                          action: () => retry.run(requestIdNum),
                        })
                      }
                    >
                      重试
                    </Button>
                  ) : null}
                  {canResetAttempts ? (
                    <Button
                      loading={resetAttempts.pending}
                      disabled={resetAttempts.pending}
                      onClick={() =>
                        confirm({
                          intent: "warning",
                          title: "确认重置尝试计数",
                          content:
                            "确定重置该请求的尝试计数？计数将清回 1（状态保持失败、不会自动重新执行），需要重新执行请再点「重试」。",
                          okText: "重置",
                          action: () => resetAttempts.run(requestIdNum),
                        })
                      }
                    >
                      重置尝试计数
                    </Button>
                  ) : null}
                  {canCancel ? (
                    <Button
                      danger
                      loading={cancel.pending}
                      disabled={cancel.pending}
                      onClick={() =>
                        confirm({
                          intent: "warning",
                          title: "确认取消请求",
                          content: "确定取消该请求？取消后不可恢复，但不会删除记录。",
                          action: () => cancel.run(requestIdNum),
                        })
                      }
                    >
                      取消请求
                    </Button>
                  ) : null}
                  {canDumpBackfill ? (
                    // 禁用按钮不触发鼠标事件：禁用原因经外层 Tooltip + span 保留
                    <Tooltip title={dumpChannelReady ? undefined : "缓存频道未配置，先在「运行设置」页配置后再转存。"}>
                      <span>
                        <Button
                          loading={dumpBackfill.pending}
                          disabled={dumpBackfill.pending || !dumpChannelReady}
                          onClick={() =>
                            confirm({
                              intent: "default",
                              title: "确认转存缓存频道",
                              content: "确定转存缓存频道？将重新获取源消息，不向用户发送任何消息。",
                              action: () => dumpBackfill.run(requestIdNum),
                            })
                          }
                        >
                          转存缓存频道
                        </Button>
                      </span>
                    </Tooltip>
                  ) : null}
                </ResponsiveActionBar>
              </Space>
            </PageSection>
          </>
        )}
      </DetailGate>
    </PageScaffold>
  );
}
