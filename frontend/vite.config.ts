import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/",
  publicDir: "../public",
  plugins: [react()],
  build: {
    assetsDir: "assets",
    manifest: true,
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // 供应商分包目标：业务发布只改页面/入口块，vendor 块内容不变、
        // 哈希稳定，配合服务端 immutable 缓存让常用依赖跨版本免重下。
        // 分组次序即优先级；charts 组必须列全图表栈专属依赖，且不能收
        // @babel/runtime/tslib 这类与 eager 图共享的 helper（会被卷成
        // eager，把入口拖回静态依赖图表大块），它们走 antd 兜底组。
        manualChunks(id) {
          if (!id.includes("node_modules")) {
            return undefined;
          }
          // react 运行时与查询库：版本升级才会变。
          if (
            /[\\/]node_modules[\\/](react|react-dom|scheduler|@tanstack)[\\/]/.test(id)
          ) {
            return "react-vendor";
          }
          // 路由库：独立成块，发布频率与业务代码解耦。
          if (/[\\/]node_modules[\\/](react-router|react-router-dom|@remix-run)[\\/]/.test(id)) {
            return "router-vendor";
          }
          // 图表栈（@antv/plots 一族及其专属传递依赖）：只被懒加载页面
          // 引用，整组合并为一个懒加载块，访问统计/总览时才下载。
          if (
            /[\\/]node_modules[\\/](@antv[\\/]|@ant-design[\\/]plots|@ant-design[\\/]charts-util|html2canvas|d3-[a-z-]+[\\/]|lodash[\\/]|gl-matrix[\\/]|internmap|color-name|is-arrayish|simple-swizzle|color-string|fecha|eventemitter3|flru|pdfast)/.test(
              id,
            )
          ) {
            return "charts-vendor";
          }
          // antd 全家（antd/rc-*/@rc-component/@ant-design/*）与其余依赖
          // 兜底：保证入口块只剩业务代码，页面块只引用稳定的 vendor 块，
          // 任何业务改动都不会级联重哈希 vendor。
          return "antd-vendor";
        },
      },
    },
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
