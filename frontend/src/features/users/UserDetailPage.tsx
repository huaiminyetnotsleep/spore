/**
 * 用户详情页（SSR /users/{id} 的 SPA 对应实现）。
 * 展示资料、状态徽标、今日用量与额度、限额、最近限流与累计请求；
 * 并接入与 SSR 表单同规则的管理操作：限额调整、状态变更
 * （启用/禁用/归档/恢复）、重置今日用量、owner 设置与 Telegram 资料刷新。
 * 破坏性操作（禁用/归档/重置/owner 变更/刷新）保留与 SSR data-confirm
 * 一致的二次确认；提交中防重复点击，失败展示服务端受控文案。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Descriptions, Form, InputNumber, Select, Spin, Tag, Typography } from "antd";
import type { InputNumberProps } from "antd";
import type { ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { fetchUserDetail } from "../../api/admin";
import {
  refreshUserProfile,
  resetUserQuota,
  setUserCloudDownload,
  setUserOwner,
  setUserStatus,
  updateUserLimits,
  type CloudDownloadMode,
  type UserStatusAction,
} from "../../api/mutations";
import {
  USER_STATUS_LABELS,
  USER_STATUS_TAG_COLORS,
  botLabel,
  fmtTime,
  labelOf,
} from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { DetailGate } from "../shared/PageStates";

const { Text } = Typography;

/** 限额表单字段（均为可选：留空保持原值，与 SSR 一致）；bind_limit 0 = 跟随角色默认。 */
interface LimitsFormValues {
  submit_interval_sec?: number;
  daily_limit?: number;
  concurrent_limit?: number;
  bind_limit?: number;
}

/** 云盘下载权限表单字段（三态，见 CloudDownloadMode）。 */
interface CloudDownloadFormValues {
  cloud_download?: CloudDownloadMode;
}

const LIMIT_INPUT_PROPS: InputNumberProps = { min: 1, className: "field-width-160" };

export function UserDetailPage() {
  const { id } = useParams();
  const userId = id ?? "";
  const userIdNum = Number(userId);
  const navigate = useNavigate();
  const query = useQuery({
    queryKey: ["users", "detail", userId],
    queryFn: () => fetchUserDetail(userId),
    enabled: Boolean(userId),
  });
  const { data: detail } = query;
  const confirm = useConfirmAction();

  const status = useAdminAction({
    action: (vars: { userId: number; action: UserStatusAction }) =>
      setUserStatus(vars.userId, vars.action),
    invalidate: [["users"], ["overview"]],
    successText: (result) => `状态已更新为${labelOf(USER_STATUS_LABELS, result.status)}。`,
  });
  const resetQuota = useAdminAction({
    action: (targetId: number) => resetUserQuota(targetId),
    invalidate: [["users"]],
    successText: "当日已用额度已重置。",
  });
  const setOwner = useAdminAction({
    action: (vars: { userId: number; owner: boolean }) =>
      setUserOwner(vars.userId, vars.owner),
    invalidate: [["users"], ["overview"]],
    successText: (result) =>
      result.is_owner
        ? "已将该用户设为 owner（原 owner 身份自动取消）"
        : "已取消该用户的 owner 身份",
  });
  const refreshProfile = useAdminAction({
    action: (targetId: number) => refreshUserProfile(targetId),
    invalidate: [["users"]],
    successText: "用户资料已刷新",
  });

  const limits = useAdminAction({
    action: (vars: { userId: number; values: LimitsFormValues }) =>
      updateUserLimits(vars.userId, vars.values),
    invalidate: [["users"]],
    successText: "限额已更新，即时生效",
  });

  const cloudDownload = useAdminAction({
    action: (vars: { userId: number; mode: CloudDownloadMode }) =>
      setUserCloudDownload(vars.userId, vars.mode),
    invalidate: [["users"]],
    successText: (result) =>
      result.effective_cloud_download
        ? "云盘下载权限已更新：当前允许该用户使用 /download。"
        : "云盘下载权限已更新：该用户使用 /download 将被拒绝。",
  });

  /**
   * 状态动作（启用/禁用、归档/恢复）：详情操作区平铺展示、正常尺寸按钮，
   * 禁用/归档是可恢复的状态变更，按 warning 意图确认（与列表页一致），
   * 按钮按语义标 danger（不改变确认弹层的 warning 意图）。
   */
  function statusButton(
    action: UserStatusAction,
    label: string,
    confirmOptions?: { title: string; content: string },
  ): ReactNode {
    return (
      <Button
        key={action}
        danger={action === "disable" || action === "archive"}
        loading={status.pending}
        disabled={status.pending}
        onClick={() => {
          if (confirmOptions) {
            confirm({
              intent: "warning",
              title: confirmOptions.title,
              content: confirmOptions.content,
              action: () => status.run({ userId: userIdNum, action }),
            });
          } else {
            void status.run({ userId: userIdNum, action });
          }
        }}
      >
        {label}
      </Button>
    );
  }

  return (
    <PageScaffold
      title="用户详情"
      description="查看资料、用量与限额，执行状态、owner、云盘下载权限与资料刷新等管理操作。"
      status={query.isFetching ? <Spin size="small" aria-label="刷新中" /> : undefined}
      actions={<Button onClick={() => void navigate("/users")}>返回列表</Button>}
    >
      {/* 详情 Gate 覆盖整页内容：数据口径等说明仅在详情加载成功后出现，
          404/错误态不泄漏业务口径说明。 */}
      <DetailGate
        loading={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
        notFoundTitle="用户不存在"
        backTo="/users"
        backText="返回用户管理"
      >
        {detail && (
          <>
            <PageSection title={`用户 ${detail.id}`}>
              <Descriptions column={1} size="small" bordered>
                <Descriptions.Item label="状态">
                  <Tag color={USER_STATUS_TAG_COLORS[detail.status]}>
                    {labelOf(USER_STATUS_LABELS, detail.status)}
                  </Tag>
                  {detail.is_owner ? <Tag color="blue">owner</Tag> : null}
                </Descriptions.Item>
                <Descriptions.Item label="用户名">
                  {detail.username ? `@${detail.username}` : "—"}
                </Descriptions.Item>
                <Descriptions.Item label="显示名">
                  {detail.display_name || "—"}
                </Descriptions.Item>
                <Descriptions.Item label="备注">{detail.note || "—"}</Descriptions.Item>
                <Descriptions.Item label="创建 / 首次使用 / 最近使用">
                  {fmtTime(detail.created_at)} / {fmtTime(detail.first_used_at)} /{" "}
                  {fmtTime(detail.last_used_at)}
                </Descriptions.Item>
                {detail.archived_at ? (
                  <Descriptions.Item label="归档时间">
                    {fmtTime(detail.archived_at)}
                  </Descriptions.Item>
                ) : null}
                <Descriptions.Item label="今日已用 / 额度">
                  {detail.used_today} / {detail.daily_limit}（剩余 {detail.remaining_today}）
                </Descriptions.Item>
                <Descriptions.Item label="限额">
                  提交间隔 {detail.submit_interval_sec} 秒 · 每日额度 {detail.daily_limit} ·
                  频道绑定{" "}
                  {detail.bind_limit > 0
                    ? detail.bind_limit
                    : `跟随默认（${detail.effective_bind_limit}）`}{" "}
                  个 ·
                  并发未完成上限 {detail.concurrent_limit}
                </Descriptions.Item>
                <Descriptions.Item label="云盘下载">
                  {detail.effective_cloud_download ? (
                    <Tag color="green">允许</Tag>
                  ) : (
                    <Tag>不允许</Tag>
                  )}
                  {detail.cloud_download === 0 ? "（跟随角色默认）" : ""}
                  <Text type="secondary"> 需「云盘下载」页全局开关同时开启</Text>
                </Descriptions.Item>
                <Descriptions.Item label="最近被拒">
                  {detail.last_denied_at
                    ? `${fmtTime(detail.last_denied_at)} ${detail.last_denied_text}`
                    : "—"}
                </Descriptions.Item>
                <Descriptions.Item label="来源机器人">
                  {botLabel(detail.source_bot_id, detail.source_bot_username)}
                  <Text type="secondary">（首次 /start 的受理机器人）</Text>
                </Descriptions.Item>
                <Descriptions.Item label="累计请求数">
                  {detail.total_requests}
                </Descriptions.Item>
              </Descriptions>
            </PageSection>

            <PageSection title="限额（即时生效；留空保持不变）">
              <Form<LimitsFormValues>
                layout="vertical"
                className="layout-max-width-480"
                initialValues={{
                  submit_interval_sec: detail.submit_interval_sec,
                  daily_limit: detail.daily_limit,
                  concurrent_limit: detail.concurrent_limit,
                  // 回显生效值：raw 0（跟随角色默认）时后端已按角色解析
                  bind_limit: detail.effective_bind_limit,
                }}
                onFinish={(values) =>
                  void limits.run({ userId: userIdNum, values: { ...values } })
                }
              >
                <Form.Item
                  name="submit_interval_sec"
                  label="提交间隔（秒，1–86400）"
                  rules={[{ type: "integer", min: 1, max: 86400, message: "提交间隔须为 1–86400 的整数。" }]}
                >
                  <InputNumber {...LIMIT_INPUT_PROPS} />
                </Form.Item>
                <Form.Item
                  name="daily_limit"
                  label="每日额度（1–100000）"
                  rules={[{ type: "integer", min: 1, max: 100000, message: "每日额度须为 1–100000 的整数。" }]}
                >
                  <InputNumber {...LIMIT_INPUT_PROPS} />
                </Form.Item>
                <Form.Item
                  name="concurrent_limit"
                  label="并发未完成任务上限（1–100）"
                  rules={[{ type: "integer", min: 1, max: 100, message: "并发上限须为 1–100 的整数。" }]}
                >
                  <InputNumber {...LIMIT_INPUT_PROPS} />
                </Form.Item>
                <Form.Item
                  name="bind_limit"
                  label="频道绑定数量上限（0–20，0 = 跟随角色默认）"
                  rules={[
                    {
                      type: "integer",
                      min: 0,
                      max: 20,
                      message: "频道绑定上限须为 0–20 的整数（0 表示跟随默认）。",
                    },
                  ]}
                  extra={`用户经 Bot /bind 绑定频道（任务成功后内容同步到其频道）；当前默认：${detail.is_owner ? "管理员 3" : "普通用户 1"} 个。`}
                >
                  <InputNumber min={0} max={20} className="field-width-160" />
                </Form.Item>
                <FormActions>
                  <Button type="primary" htmlType="submit" loading={limits.pending}>
                    保存限额
                  </Button>
                </FormActions>
              </Form>
            </PageSection>

            <PageSection title="云盘下载权限">
              <Form<CloudDownloadFormValues>
                layout="vertical"
                className="layout-max-width-480"
                initialValues={{ cloud_download: detail.cloud_download }}
                onFinish={(values) =>
                  void cloudDownload.run({
                    userId: userIdNum,
                    mode: values.cloud_download ?? 0,
                  })
                }
              >
                <Form.Item
                  name="cloud_download"
                  label="是否允许该用户使用 /download（Bot 云盘下载）"
                  extra={`跟随角色默认时：${detail.is_owner ? "owner 默认允许" : "普通用户默认不允许"}；全局开关关闭时所有人不可用；管理员补存不受此设置限制。`}
                >
                  <Select
                    aria-label="云盘下载权限"
                    virtual={false}
                    options={[
                      { value: 0, label: "跟随角色默认" },
                      { value: 1, label: "允许" },
                      { value: 2, label: "拒绝" },
                    ]}
                  />
                </Form.Item>
                <FormActions>
                  <Button type="primary" htmlType="submit" loading={cloudDownload.pending}>
                    保存权限
                  </Button>
                </FormActions>
              </Form>
            </PageSection>

            <PageSection title="操作">
              {/* 详情页空间充足：操作全部平铺为正常尺寸按钮（不用「更多」菜单、
                  不用文字按钮）。查看入口原在资料行「累计请求数」内，现与状态、
                  限额、owner、云盘权限、资料刷新聚合在同一操作区；窄屏由
                  ResponsiveActionBar 换行。确认意图与 pending 语义不变。 */}
              <ResponsiveActionBar align="start">
                <Button onClick={() => void navigate(`/requests?user_id=${detail.id}`)}>
                  查看请求记录
                </Button>
                <Button onClick={() => void navigate(`/channel-bindings?user_id=${detail.id}`)}>
                  查看频道绑定
                </Button>
                {detail.status !== "enabled"
                  ? statusButton("enable", "启用")
                  : statusButton("disable", "禁用", {
                      title: "确认禁用用户",
                      content: "确定禁用该用户？其新请求将被立即拒绝。",
                    })}
                {detail.status !== "archived"
                  ? statusButton("archive", "归档", {
                      title: "确认归档用户",
                      content: "确定归档该用户？立即失去权限，历史记录与统计保留，可恢复。",
                    })
                  : statusButton("restore", "恢复（重新启用）")}
                <Button
                  loading={resetQuota.pending}
                  disabled={resetQuota.pending}
                  onClick={() =>
                    confirm({
                      intent: "default",
                      title: "确认重置今日用量",
                      content: "确定重置该用户今日已用额度为 0？",
                      action: () => resetQuota.run(userIdNum),
                    })
                  }
                >
                  重置今日用量
                </Button>
                {detail.is_owner
                  ? ownerButton("取消 owner", "确认取消 owner", "确定取消该用户的 owner 身份？取消后其请求将受频率与额度限制。", false)
                  : ownerButton("设为 owner", "确认设置 owner", "确定将该用户设为 owner？原 owner（如有）身份将被取消。", true)}
                <Button
                  loading={refreshProfile.pending}
                  disabled={refreshProfile.pending}
                  onClick={() =>
                    confirm({
                      intent: "default",
                      title: "确认刷新 Telegram 资料",
                      content: "确定从当前 Telegram 上下文刷新该用户资料？查询失败不会覆盖现有资料。",
                      action: () => refreshProfile.run(userIdNum),
                    })
                  }
                >
                  刷新 Telegram 资料
                </Button>
              </ResponsiveActionBar>
              <div className="layout-margin-block-start-12">
                <Text type="secondary">
                  owner 不受频率与每日额度限制，请求仍完整记录；全部操作写入审计。
                </Text>
              </div>
            </PageSection>

            <PageSection title="数据口径">
              <Text type="secondary">
                今日已用按运营时区当日统计；owner 请求不受频率与每日额度限制，仍完整记录。
              </Text>
            </PageSection>
          </>
        )}
      </DetailGate>
    </PageScaffold>
  );

  /** owner 动作：设为/取消均为可恢复的权限变更，按 warning 意图二次确认。 */
  function ownerButton(label: string, confirmTitle: string, confirmContent: string, owner: boolean): ReactNode {
    return (
      <Button
        key={owner ? "set-owner" : "unset-owner"}
        loading={setOwner.pending}
        disabled={setOwner.pending}
        onClick={() =>
          confirm({
            intent: "warning",
            title: confirmTitle,
            content: confirmContent,
            action: () => setOwner.run({ userId: userIdNum, owner }),
          })
        }
      >
        {label}
      </Button>
    );
  }
}
