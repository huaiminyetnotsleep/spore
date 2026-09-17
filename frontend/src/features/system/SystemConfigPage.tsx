/**
 * 系统设置页（系统身份，与「运行设置」的运营调优分离）。
 * 本期提供系统名称（品牌名）配置：保存后 Bot 帮助/欢迎文案、事件通知标题
 * 与审批通知即时生效；管理端品牌栏/登录页/浏览器标题随 SPA 壳注入的
 * <meta name="app-name"> 在下次刷新后更新。缺省名称为 Spore（服务端回退）。
 * 保存按钮在表单变更（dirty）后才可用，并以「未保存更改」提示当前状态。
 */
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Input, Space, Typography } from "antd";
import { Link } from "react-router-dom";

import { fetchSystemConfig } from "../../api/admin";
import { saveSystemConfig } from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageScaffold } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";

const { Text } = Typography;

interface SystemConfigFormValues {
  system_name?: string;
}

export function SystemConfigPage() {
  const [form] = Form.useForm<SystemConfigFormValues>();
  const [dirty, setDirty] = useState(false);
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
    onDone: () => setDirty(false),
  });

  return (
    <PageScaffold
      title="系统设置"
      description="系统身份配置（本期仅系统名称）；运行调优与频道配置在对应业务页维护。"
    >
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="middle" className="field-width-full">
          <Form<SystemConfigFormValues>
            form={form}
            layout="vertical"
            className="layout-max-width-520"
            disabled={isPending}
            onValuesChange={() => setDirty(true)}
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

            <FormActions>
              {dirty ? <Text type="warning">有未保存的更改</Text> : null}
              <Button type="primary" htmlType="submit" loading={save.pending} disabled={!dirty}>
                保存
              </Button>
            </FormActions>
            {/* 服务端 400 等受控文案统一经 useAdminAction 的 message.error 提示 */}
          </Form>

          {/* 说明区不再套 Card：当前值 + 跳转说明直接以文字呈现 */}
          <div>
            <Text>当前名称：{data?.system_name ?? "…"}</Text>
            <br />
            <Text type="secondary">
              系统设置只承载系统身份（本期仅系统名称）；队列容量、媒体传输参数等运行调优在
              <Link to="/settings">运行设置</Link>
              页，频道同步与频道加入配置分别在
              <Link to="/channel-settings">频道设置</Link>、
              <Link to="/join-settings">受邀设置</Link>
              页维护。
            </Text>
          </div>
        </Space>
      </PageQueryState>
    </PageScaffold>
  );
}
