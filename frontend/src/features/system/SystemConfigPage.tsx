/**
 * 系统设置页（系统身份，与「运行设置」的运营调优分离）。
 * 本期提供系统名称（品牌名）配置：保存后 Bot 帮助/欢迎文案、事件通知标题
 * 与审批通知即时生效；管理端品牌栏/登录页/浏览器标题随 SPA 壳注入的
 * <meta name="app-name"> 在下次刷新后更新。缺省名称为 Spore（服务端回退）。
 */
import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Input, Space, Typography } from "antd";

import { fetchSystemConfig } from "../../api/admin";
import { saveSystemConfig } from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { LoadError, PageCard, SectionCard } from "../shared/PageStates";

const { Text } = Typography;

interface SystemConfigFormValues {
  system_name?: string;
}

export function SystemConfigPage() {
  const [form] = Form.useForm<SystemConfigFormValues>();
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["system", "config"],
    queryFn: fetchSystemConfig,
  });

  // Ant Design 的 initialValues 只在首次挂载时读取；配置数据异步返回后
  // 必须显式回填，避免页面显示为空而保存时误提交空值。
  useEffect(() => {
    if (!data) return;
    form.setFieldsValue({ system_name: data.system_name });
  }, [data, form]);

  const save = useAdminAction({
    action: (values: SystemConfigFormValues) =>
      saveSystemConfig((values.system_name ?? "").trim()),
    invalidate: [["system", "config"]],
    successText:
      "系统名称已保存，Bot 文案即时生效；管理端品牌栏与浏览器标题在刷新页面后更新。",
  });

  if (isError) {
    return (
      <PageCard title="系统设置">
        <LoadError onRetry={() => void refetch()} />
      </PageCard>
    );
  }

  return (
    <PageCard title="系统设置">
      <Space direction="vertical" size="middle" className="field-width-full">
        <Form<SystemConfigFormValues>
          form={form}
          layout="vertical"
          className="layout-max-width-520"
          disabled={isPending}
          onFinish={(values) => void save.run(values)}
        >
          <Form.Item
            name="system_name"
            label="系统名称（即时生效）"
            rules={[
              { required: true, whitespace: true, message: "系统名称不能为空。" },
              { max: 32, message: "系统名称不能超过 32 个字符。" },
            ]}
            extra="用于 Bot 帮助/欢迎文案、事件通知标题、审批通知与管理端品牌栏、登录页、浏览器标题；未配置时为 Spore。"
          >
            <Input placeholder="如 Spore" allowClear maxLength={32} />
          </Form.Item>

          <Button type="primary" htmlType="submit" loading={save.pending}>
            保存
          </Button>
          {/* 服务端 400 等受控文案统一经 useAdminAction 的 message.error 提示 */}
        </Form>

        <SectionCard title="说明">
          <Space direction="vertical" size="small">
            <Text>当前名称：{data?.system_name ?? "…"}</Text>
            <Text type="secondary">
              系统设置只承载系统身份（本期仅系统名称）；队列容量、媒体传输参数等运行调优在「运行设置」页，频道同步与频道加入配置分别在「频道设置」「受邀设置」页维护。
            </Text>
          </Space>
        </SectionCard>
      </Space>
    </PageCard>
  );
}
