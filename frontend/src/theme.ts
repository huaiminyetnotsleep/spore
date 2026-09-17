import type { ThemeConfig } from "antd";

export const designTokens = {
  colorPrimary: "#1677ff",
  colorSuccess: "#52c41a",
  colorWarning: "#fa8c16",
  colorError: "#ff4d4f",
  colorInfo: "#1677ff",
  colorText: "#1f1f1f",
  colorTextSecondary: "#595959",
  colorBgLayout: "#f5f7fa",
  colorBgContainer: "#ffffff",
  colorFillSecondary: "#fafafa",
  colorBorder: "#d9d9d9",
  colorBorderSecondary: "#f0f0f0",
  borderRadius: 8,
  borderRadiusLG: 10,
  boxShadowSecondary: "0 6px 16px rgb(0 0 0 / 8%)",
  fontFamily:
    'system-ui, -apple-system, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif',
} as const;

export const statusPalette = {
  success: designTokens.colorSuccess,
  warning: designTokens.colorWarning,
  error: designTokens.colorError,
  processing: designTokens.colorInfo,
  inactive: "#8c8c8c",
  successBackground: "#f6ffed",
  warningBackground: "#fff7e6",
  errorBackground: "#fff2f0",
  processingBackground: "#e6f4ff",
  inactiveBackground: "#fafafa",
} as const;

export const chartPalette = {
  primary: designTokens.colorPrimary,
  success: designTokens.colorSuccess,
  warning: designTokens.colorWarning,
  error: designTokens.colorError,
  purple: "#722ed1",
  cyan: "#13c2c2",
  neutral: statusPalette.inactive,
  series: [
    designTokens.colorPrimary,
    designTokens.colorSuccess,
    "#722ed1",
    "#13c2c2",
    designTokens.colorWarning,
    designTokens.colorError,
  ],
} as const;

/**
 * The single Ant Design theme entry. cssVar mode intentionally stays disabled:
 * the existing css-in-js + CSP nonce path remains unchanged.
 */
export const appTheme: ThemeConfig = {
  token: {
    colorPrimary: designTokens.colorPrimary,
    colorSuccess: designTokens.colorSuccess,
    colorWarning: designTokens.colorWarning,
    colorError: designTokens.colorError,
    colorInfo: designTokens.colorInfo,
    colorText: designTokens.colorText,
    colorTextSecondary: designTokens.colorTextSecondary,
    colorBgLayout: designTokens.colorBgLayout,
    colorBgContainer: designTokens.colorBgContainer,
    colorFillSecondary: designTokens.colorFillSecondary,
    colorBorder: designTokens.colorBorder,
    colorBorderSecondary: designTokens.colorBorderSecondary,
    borderRadius: designTokens.borderRadius,
    borderRadiusLG: designTokens.borderRadiusLG,
    boxShadowSecondary: designTokens.boxShadowSecondary,
    fontFamily: designTokens.fontFamily,
    controlHeight: 36,
    controlHeightSM: 28,
  },
  components: {
    Button: {
      fontWeight: 500,
    },
    Card: {
      headerFontSize: 16,
      headerHeight: 52,
    },
    Descriptions: {
      labelBg: designTokens.colorFillSecondary,
    },
    Form: {
      itemMarginBottom: 16,
      verticalLabelPadding: "0 0 6px",
    },
    Modal: {
      titleFontSize: 18,
    },
    Table: {
      cellPaddingBlock: 12,
      cellPaddingInline: 12,
      headerBg: designTokens.colorFillSecondary,
      rowHoverBg: "#f5faff",
    },
  },
};
