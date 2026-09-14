/**
 * SPA 登录页（/admin/login 公开路由；旧 SSR /login 已删除）。
 * 独立于管理端主布局（无侧边栏/头部）：未登录状态下会话引导接口不可用，
 * 本页只经公开的 /api/v1/login/csrf 与 /api/v1/login 完成密钥登录。
 * 提交流程：取登录级 CSRF token（服务端同时种双提交 Cookie）→ POST 登录
 * → 成功后跳转管理端入口（应用内 /，浏览器地址 /admin）；失败展示服务端
 * 受控中文文案（401 密钥错误 / 403 CSRF 失败 / 429 限流锁定）。
 * GitHub 登录入口沿用既有 /auth/github 流程，仅在服务端报告通道已配置时展示。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Card, Form, Input, Typography } from "antd";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import { fetchLoginCSRF, loginWithAccessKey } from "../../api/session";
import { appName } from "../../shared/appName";
import { errorText } from "../shared/actions";

const { Text } = Typography;

interface LoginFormValues {
  accessKey: string;
}

export function LoginPage() {
  const navigate = useNavigate();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [form] = Form.useForm<LoginFormValues>();

  // 登录壳独立于主布局：浏览器标题在这里同步一次（系统名称由壳注入的
  // <meta name="app-name"> 提供）。
  useEffect(() => {
    document.title = appName();
  }, []);

  // GitHub 入口显隐跟随服务端配置（沿用旧 SSR 登录页语义）；查询同时
  // 预热登录 CSRF，提交时仍会再取一次（同一 Cookie 返回同一 token）。
  const { data: csrf } = useQuery({
    queryKey: ["login", "csrf"],
    queryFn: fetchLoginCSRF,
    staleTime: 30 * 60 * 1000,
  });

  const submit = async (values: LoginFormValues) => {
    setPending(true);
    setError("");
    try {
      const loginCSRF = await fetchLoginCSRF();
      await loginWithAccessKey(values.accessKey, loginCSRF.csrf_token);
      // 路由路径相对 basename /admin：应用内 "/" 即浏览器地址 /admin（总览）
      navigate("/", { replace: true });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="login-shell">
      <div className="login-brand" aria-hidden="true">
        <img className="login-brand-mark" src="/icon.svg" alt="" />
      </div>
      <Card className="login-card" title={`登录 ${appName()}`}>
        <Form<LoginFormValues>
          form={form}
          layout="vertical"
          requiredMark={false}
          disabled={pending}
          onFinish={(values) => void submit(values)}
        >
          <Form.Item
            name="accessKey"
            label="访问密钥"
            rules={[{ required: true, message: "请输入访问密钥。" }]}
          >
            <Input.Password
              autoFocus
              autoComplete="current-password"
              placeholder="访问密钥"
            />
          </Form.Item>
          {error ? (
            <Text type="danger" role="alert" className="login-error">
              {error}
            </Text>
          ) : null}
          <Form.Item className="layout-margin-bottom-0">
            <Button type="primary" htmlType="submit" block loading={pending}>
              登录
            </Button>
          </Form.Item>
        </Form>
        {csrf?.github_enabled ? (
          <>
            <Text type="secondary" className="login-hint">
              也可以使用已绑定的 GitHub 账号登录：
            </Text>
            <a className="login-github-link" href="/auth/github">
              使用 GitHub 登录
            </a>
          </>
        ) : null}
      </Card>
    </div>
  );
}
