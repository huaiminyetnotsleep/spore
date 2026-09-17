/**
 * MTProto 扫码登录页（SSR /mtproto/login 的 SPA 对应实现）。
 * 状态经 /api/v1/mtproto/status 轮询（与 SSR admin.js 同为 2 秒；页面失焦或
 * 卸载时自动暂停/停止）。二维码继续用既有同源 GET /mtproto/qr.png 图片端点
 * 展示（img 引用，响应 no-store）；API 不下发扫码 URL 本身，前端也不保存
 * 任何敏感值。二维码加载失败时展示受控提示，并在新码生成（updated_at 变化）
 * 后自动重试。重连按钮仅离线可用，先经 warning 意图确认弹层（重连会重启
 * 客户端、期间 Bot 暂停服务，可恢复但影响运行，不属于 danger）。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Space, Typography } from "antd";
import { useEffect, useState } from "react";

import { fetchMTProtoStatus } from "../../api/admin";
import { reloginMTProto } from "../../api/mutations";
import { MTPROTO_STATE_LABELS, labelOf } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { StatusTag, type StatusTone } from "../shared/StatusTag";

const { Text, Paragraph } = Typography;

/** 轮询间隔与 SSR admin.js 一致（2 秒）。 */
const STATUS_POLL_INTERVAL_MS = 2000;

/** SSR data-confirm 同款文案。 */
const CONFIRM_RELOGIN_TEXT =
  "确定触发重连？登录模式将重启 MTProto 客户端，期间 Bot 暂停服务。";

/** 会话状态 → 语义色调：ready 为已连接，其余按未连接展示。 */
function connectionTone(state: string): StatusTone {
  return state === "ready" ? "success" : "error";
}

export function MTProtoPage() {
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["mtproto", "status"],
    queryFn: fetchMTProtoStatus,
    refetchInterval: STATUS_POLL_INTERVAL_MS,
    // 默认不在页面失焦时后台轮询，卸载即停止（与 SSR 行为对齐）
    refetchIntervalInBackground: false,
  });
  // qr.png 加载失败（如扫码 URL 已轮换导致瞬时 404）时的受控提示；
  // updated_at 变化说明二维码已更新，恢复 img 以便用新地址重新加载
  const [qrFailed, setQrFailed] = useState(false);
  const qrUpdatedAt = data?.updated_at;
  useEffect(() => {
    setQrFailed(false);
  }, [qrUpdatedAt]);

  const relogin = useAdminAction({
    action: () => reloginMTProto(),
    invalidate: [["mtproto"]],
    successText: "已触发重连，请等待新的扫码二维码。",
  });
  const confirm = useConfirmAction();

  const connected = data?.state === "ready";
  const canRelogin = data?.state === "offline";

  return (
    <PageScaffold
      title="Telegram 连接"
      description="查看 MTProto 登录会话与各机器人直传状态；仅离线时支持重连 / 重新扫码登录。"
    >
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        {/* unknown 仅在服务未接入 MTProto 会话时出现（与 SSR "未接入"一致） */}
        {data && data.state === "unknown" ? (
          <Paragraph type="secondary">本实例未接入 MTProto 登录会话，状态不可用。</Paragraph>
        ) : (
          <Space direction="vertical" size="middle" className="field-width-full">
            <PageSection title="会话状态">
              <Space direction="vertical" size="small" className="field-width-full">
                <Space size="small" wrap>
                  <Text>当前状态：</Text>
                  <Text strong data-testid="mtproto-state">
                    {data ? labelOf(MTPROTO_STATE_LABELS, data.state) : "…"}
                  </Text>
                  {data ? (
                    <StatusTag tone={connectionTone(data.state)}>
                      {connected ? "已连接" : "未连接"}
                    </StatusTag>
                  ) : null}
                </Space>
                <Space size="small" wrap>
                  <Text>机器人 Bot 会话：</Text>
                  <Text strong>{data?.bot_state === "ready" ? "就绪" : data?.bot_state ? "离线" : "未接入"}</Text>
                  {data?.bot_state ? (
                    <StatusTag tone={connectionTone(data.bot_state)}>
                      {data.bot_state === "ready" ? "已连接" : "未连接"}
                    </StatusTag>
                  ) : null}
                  {data?.bot_dc_id ? <StatusTag tone="processing">DC {data.bot_dc_id}</StatusTag> : null}
                </Space>

                {data?.bots && data.bots.length > 1 ? (
                  <Space size="small" wrap data-testid="mtproto-bot-list">
                    <Text>各机器人直传会话：</Text>
                    {data.bots.map((bot) => (
                      <Space key={bot.bot_id} size={4}>
                        <Text type="secondary">{bot.username ? `@${bot.username}` : `bot ${bot.bot_id}`}</Text>
                        <StatusTag tone={connectionTone(bot.state)}>
                          {bot.state === "ready" ? `已连接${bot.dc_id ? ` · DC ${bot.dc_id}` : ""}` : "未连接"}
                        </StatusTag>
                      </Space>
                    ))}
                  </Space>
                ) : null}

                {data?.last_error ? <Alert type="error" showIcon message={data.last_error} /> : null}
              </Space>
            </PageSection>

            {data?.qr_available ? (
              <PageSection title="扫码登录" className="layout-max-width-320">
                <Space direction="vertical" size="small" className="field-width-full">
                  <Text>打开手机 Telegram → 设置 → 设备 → 关联桌面设备，扫描下方二维码：</Text>
                  {/* 同源图片端点：no-store；v 参数（状态更新时间）保证新码重新取图 */}
                  {qrFailed ? (
                    <Text type="warning">二维码图片加载失败，新码生成后将自动重试。</Text>
                  ) : (
                    <img
                      data-testid="mtproto-qr"
                      alt="登录二维码"
                      src={`/mtproto/qr.png?v=${data.updated_at}`}
                      width={240}
                      height={240}
                      onError={() => setQrFailed(true)}
                    />
                  )}
                  <Text type="secondary">二维码约 30 秒自动刷新，页面无需手动操作。</Text>
                </Space>
              </PageSection>
            ) : null}

            <PageSection title="重连">
              <Space direction="vertical" size="small" className="field-width-full">
                <div>
                  <Button
                    type="primary"
                    disabled={!canRelogin}
                    loading={relogin.pending}
                    onClick={() =>
                      confirm({
                        intent: "warning",
                        title: "确认重连 / 重新扫码登录",
                        content: CONFIRM_RELOGIN_TEXT,
                        action: () => relogin.run(undefined),
                      })
                    }
                  >
                    重连 / 重新扫码登录
                  </Button>
                </div>
                <Paragraph type="secondary" className="layout-margin-bottom-0">
                  仅离线状态可触发重连；Web 通道只走扫码登录（验证码/2FA 需在服务器终端完成）。
                  Web 登录失败时，重启进程即可回到终端扫码流程，两条路径互不影响。
                </Paragraph>
              </Space>
            </PageSection>
          </Space>
        )}
      </PageQueryState>
    </PageScaffold>
  );
}
