/**
 * MTProto 扫码登录页（SSR /mtproto/login 的 SPA 对应实现）。
 * 状态经 /api/v1/mtproto/status 轮询（与 SSR admin.js 同为 2 秒；页面失焦或
 * 卸载时自动暂停/停止）。二维码继续用既有同源 GET /mtproto/qr.png 图片端点
 * 展示（img 引用，响应 no-store）；API 不下发扫码 URL 本身，前端也不保存
 * 任何敏感值。二维码加载失败时展示受控提示，并在新码生成（updated_at 变化）
 * 后自动重试。重连按钮仅离线可用，先经确认弹层（文案与 SSR data-confirm 一致）。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Space, Tag, Typography } from "antd";
import { useEffect, useState } from "react";

import { fetchMTProtoStatus } from "../../api/admin";
import { reloginMTProto } from "../../api/mutations";
import { MTPROTO_STATE_LABELS, labelOf } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { LoadError, PageCard, SectionCard } from "../shared/PageStates";

const { Text, Paragraph } = Typography;

/** 轮询间隔与 SSR admin.js 一致（2 秒）。 */
const STATUS_POLL_INTERVAL_MS = 2000;

/** SSR data-confirm 同款文案。 */
const CONFIRM_RELOGIN_TEXT =
  "确定触发重连？登录模式将重启 MTProto 客户端，期间 Bot 暂停服务。";

export function MTProtoPage() {
  const { data, isError, refetch } = useQuery({
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
  const confirmAction = useConfirmAction();

  if (isError) {
    return (
      <PageCard title="Telegram 连接">
        <LoadError onRetry={() => void refetch()} />
      </PageCard>
    );
  }

  // unknown 仅在服务未接入 MTProto 会话时出现（与 SSR "未接入"一致）
  if (data && data.state === "unknown") {
    return (
      <PageCard title="Telegram 连接">
        <Paragraph type="secondary">本实例未接入 MTProto 登录会话，状态不可用。</Paragraph>
      </PageCard>
    );
  }

  const connected = data?.state === "ready";
  const canRelogin = data?.state === "offline";

  return (
    <PageCard title="Telegram 连接">
      <Space direction="vertical" size="middle" className="field-width-full">
        <Space size="small">
          <Text>当前状态：</Text>
          <Text strong data-testid="mtproto-state">
            {data ? labelOf(MTPROTO_STATE_LABELS, data.state) : "…"}
          </Text>
          {data ? (
            connected ? (
              <Tag color="green">已连接</Tag>
            ) : (
              <Tag color="red">未连接</Tag>
            )
          ) : null}
        </Space>
        <Space size="small">
          <Text>机器人 Bot 会话：</Text>
          <Text strong>{data?.bot_state === "ready" ? "就绪" : data?.bot_state ? "离线" : "未接入"}</Text>
          {data?.bot_state ? (
            data.bot_state === "ready" ? <Tag color="green">已连接</Tag> : <Tag color="red">未连接</Tag>
          ) : null}
          {data?.bot_dc_id ? <Tag color="blue">DC {data.bot_dc_id}</Tag> : null}
        </Space>

        {data?.last_error ? <Alert type="error" showIcon message={data.last_error} /> : null}

        {data?.qr_available ? (
          <SectionCard className="layout-max-width-320">
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
          </SectionCard>
        ) : null}

        <div>
          <Button
            type="primary"
            disabled={!canRelogin}
            loading={relogin.pending}
            onClick={() => confirmAction(CONFIRM_RELOGIN_TEXT, () => void relogin.run(undefined))}
          >
            重连 / 重新扫码登录
          </Button>
        </div>

        <Paragraph type="secondary" className="layout-margin-bottom-0">
          仅离线状态可触发重连；Web 通道只走扫码登录（验证码/2FA 需在服务器终端完成）。
          Web 登录失败时，重启进程即可回到终端扫码流程，两条路径互不影响。
        </Paragraph>
      </Space>
    </PageCard>
  );
}
