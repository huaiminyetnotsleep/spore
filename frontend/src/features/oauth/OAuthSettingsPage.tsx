/**
 * GitHub 登录配置页（SSR /settings/oauth 的 SPA 对应实现）。
 * 配置（save/enable/disable/clear）与绑定/解绑复用 /api/v1 共用核心；
 * Client Secret 只在提交瞬间存在于内存，不回显、不落 localStorage；
 * 解绑后全部会话失效，页面立即跳转登录页。bind 返回一次性授权跳转
 * URL，由本页 window.location 跳转，回调沿用既有 /auth/github/callback。
 * 当前回调不再渲染 HTML 结果页：服务端 302 回本页并携带受控 oauth
 * 查询参数（bound/error），本页读取后展示一次性横幅并清除参数。
 * 清除凭据与解绑账号统一走 danger 二次确认（Promise 感知 pending）。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Checkbox, Form, Input, Space, Tag, Typography } from "antd";
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";

import { fetchOAuthSettings } from "../../api/admin";
import {
  bindOAuth,
  saveOAuthSettings,
  unbindOAuth,
  type OAuthSettingsAction,
} from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";

const { Text, Paragraph } = Typography;

interface OAuthFormValues {
  client_id?: string;
  client_secret?: string;
  enabled?: boolean;
}

/** 一次配置变更载荷：action 由触发入口决定（表单提交为 save）。 */
type OAuthActionInput = Partial<OAuthFormValues> & { action: OAuthSettingsAction };

/** OAuth 回调结果横幅：文案映射在 SPA 内维护，不透传任何查询串原文。 */
const oauthResultBanners: Record<"bound" | "error", { type: "success" | "error"; message: string }> = {
  bound: {
    type: "success",
    message: "GitHub 登录通道已绑定。为安全起见，全部登录会话已失效，请重新登录。",
  },
  error: {
    type: "error",
    message: "GitHub 授权流程未完成，请稍后重试。",
  },
};

/** 读取 oauth 查询参数并转为横幅状态；非受控枚举值一律忽略。 */
function useOAuthResultBanner() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [banner, setBanner] = useState<keyof typeof oauthResultBanners | null>(null);

  useEffect(() => {
    const value = searchParams.get("oauth");
    if (value === "bound" || value === "error") {
      setBanner(value);
      // 横幅状态已留存，立即清除参数（replace 不新增历史记录），
      // 刷新页面后不会重复弹出结果提示。
      setSearchParams({}, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  return { banner, clear: () => setBanner(null) };
}

export function OAuthSettingsPage() {
  const [form] = Form.useForm<OAuthFormValues>();
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["oauth"],
    queryFn: fetchOAuthSettings,
  });
  const { banner, clear } = useOAuthResultBanner();
  const confirm = useConfirmAction();

  // 表单初值跟随服务端配置（Secret 永不回显，仅占位提示留空保留；
  // enabled 一并回显，否则刷新后勾选框永远显示未选中）
  useEffect(() => {
    if (data) {
      form.setFieldsValue({
        client_id: data.client_id,
        client_secret: "",
        enabled: data.enabled,
      });
    }
  }, [data, form]);

  const save = useAdminAction({
    action: (values: OAuthActionInput) => {
      if (values.action === "save") {
        return saveOAuthSettings({
          action: "save",
          client_id: values.client_id?.trim() ?? "",
          client_secret: values.client_secret ?? "",
          enabled: values.enabled ?? false,
        });
      }
      return saveOAuthSettings({ action: values.action });
    },
    invalidate: [["oauth"], ["overview"]],
    successText: "GitHub 登录配置已保存。新 OAuth 登录会立即使用最新配置。",
    onDone: (_data, vars) => {
      if (vars.action === "save") {
        form.setFieldValue("client_secret", "");
      }
    },
  });

  const bind = useAdminAction({
    action: () => bindOAuth(),
    invalidate: [["oauth"]],
    successText: "正在跳转到 GitHub 授权页面…",
    onDone: (result) => window.location.assign(result.authorize_url),
  });

  const unbind = useAdminAction({
    action: () => unbindOAuth(),
    invalidate: [["oauth"]],
    successText: "已解绑，全部登录会话已失效。",
    onDone: () => window.location.assign("/admin/login"),
  });

  return (
    <PageScaffold
      title="GitHub 登录"
      description="配置 GitHub OAuth 登录通道并绑定管理员账号；Client Secret 加密保存且永不回显。"
    >
      {banner ? (
        <Alert
          type={oauthResultBanners[banner].type}
          showIcon
          closable
          message={oauthResultBanners[banner].message}
          onClose={clear}
        />
      ) : null}
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="middle" className="field-width-full">
          <PageSection
            title="登录配置"
            extra={
              data ? (
                data.github_configured ? (
                  <Tag color="green">已启用</Tag>
                ) : (
                  <Tag color="gold">未启用</Tag>
                )
              ) : null
            }
          >
            {data?.read_error ? <Alert type="error" showIcon message={data.read_error} /> : null}
            <Form<OAuthFormValues>
              form={form}
              layout="vertical"
              className="layout-max-width-480"
              onFinish={(values) => void save.run({ ...values, action: "save" })}
            >
              <Form.Item
                name="client_id"
                label="Client ID"
                extra={
                  data ? (
                    <Text type="secondary">
                      当前 Client ID：{data.client_id || "未配置"}；Secret：
                      {data.secret_set ? "已设置" : "未设置"}
                    </Text>
                  ) : null
                }
              >
                <Input placeholder="GitHub OAuth Client ID" allowClear />
              </Form.Item>
              <Form.Item name="client_secret" label="Client Secret">
                <Input.Password autoComplete="new-password" placeholder="留空则保留已保存 Secret" />
              </Form.Item>
              <Form.Item name="enabled" valuePropName="checked" className="layout-margin-bottom-12">
                <Checkbox>启用 GitHub 登录</Checkbox>
              </Form.Item>
              <FormActions>
                <Button type="primary" htmlType="submit" loading={save.pending}>
                  保存配置
                </Button>
              </FormActions>
            </Form>
            <Paragraph type="secondary" className="layout-margin-top-12">
              Client Secret 使用 WEB_OAUTH_ENCRYPTION_KEY 加密保存；页面、日志、审计和备份不显示明文。
            </Paragraph>
            {data?.configured ? (
              <Space wrap>
                <Button
                  loading={save.pending}
                  onClick={() => void save.run({ action: data.enabled ? "disable" : "enable" })}
                >
                  {data.enabled ? "停用登录入口" : "启用登录入口"}
                </Button>
                <Button
                  danger
                  loading={save.pending}
                  onClick={() =>
                    confirm({
                      intent: "danger",
                      title: "确认清除 GitHub 登录凭据",
                      content: "确定清除 GitHub Client ID 与 Secret？此操作不会影响访问密钥登录。",
                      action: () => save.run({ action: "clear" }),
                    })
                  }
                >
                  清除凭据
                </Button>
              </Space>
            ) : null}
          </PageSection>

          <PageSection title="绑定管理员账号">
            {data?.bound ? (
              <Space direction="vertical" size="small" className="field-width-full">
                <Text>
                  当前绑定账号：<Text strong>{data.github_login}</Text>（GitHub ID {data.github_id}
                  ），绑定时间 {fmtTime(data.bound_at)}。
                </Text>
                <Text type="secondary">
                  仅该 GitHub 账号可经 OAuth 登录管理端。解绑后全部会话立即失效。
                </Text>
                {/* 解绑使全部会话失效：触发按钮与确认均为 danger 语义 */}
                <Button
                  danger
                  loading={unbind.pending}
                  onClick={() =>
                    confirm({
                      intent: "danger",
                      title: "确认解绑 GitHub 账号",
                      content: "确定解绑 GitHub 账号？全部会话将立即失效。",
                      action: () => unbind.run(undefined),
                    })
                  }
                >
                  解绑 GitHub 账号
                </Button>
              </Space>
            ) : (
              <Space direction="vertical" size="small">
                <Text>尚未绑定 GitHub 账号。绑定后仅该账号可使用 OAuth 登录。</Text>
                <Button
                  type="primary"
                  disabled={!data?.github_configured}
                  loading={bind.pending}
                  onClick={() => void bind.run(undefined)}
                >
                  绑定 GitHub 账号
                </Button>
              </Space>
            )}
          </PageSection>
        </Space>
      </PageQueryState>
    </PageScaffold>
  );
}
