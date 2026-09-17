import { StyleProvider } from "@ant-design/cssinjs";
import { QueryClientProvider } from "@tanstack/react-query";
import { ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { queryClient } from "./api/query";
import { App } from "./App";
import { cspConfig, readCSPNonce } from "./csp";
import { appTheme } from "./theme";
import "./styles.css";

const cspNonce = readCSPNonce();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* 当前 cssinjs 运行时会透传 nonce，但 1.24.0 类型声明尚未暴露该属性；ConfigProvider csp 同时覆盖 antd 内部注册链路。 */}
      {/* @ts-expect-error StyleProvider 的运行时 nonce 支持未同步到当前类型声明。 */}
      <StyleProvider nonce={cspNonce}>
        <ConfigProvider
          locale={zhCN}
          csp={cspConfig(cspNonce)}
          theme={appTheme}
        >
          <App />
        </ConfigProvider>
      </StyleProvider>
    </QueryClientProvider>
  </StrictMode>,
);
