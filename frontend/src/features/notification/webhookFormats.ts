import type {
  NotificationMentionMode,
  NotificationWebhookFormat,
} from "../../api/admin";
export type WebhookTextFieldKey =
  | "generic_signature_header"
  | "open_ids"
  | "mobiles"
  | "user_ids"
  | "role_ids";

export interface WebhookTextFieldDescriptor {
  key: WebhookTextFieldKey;
  label: string;
  placeholder: string;
  extra: string;
}

export interface WebhookMentionOption {
  value: NotificationMentionMode;
  label: string;
  fieldKey?: WebhookTextFieldKey;
}

export interface WebhookFormatDescriptor {
  value: NotificationWebhookFormat;
  label: string;
  description: string;
  urlPlaceholder: string;
  secret?: {
    label: string;
    placeholder: string;
    extra: string;
  };
  mentionOptions: WebhookMentionOption[];
  alwaysFields?: WebhookTextFieldKey[];
  textFields: Partial<Record<WebhookTextFieldKey, WebhookTextFieldDescriptor>>;
  defaults: { mention_mode: NotificationMentionMode };
}

export const WEBHOOK_FORMATS: readonly WebhookFormatDescriptor[] = [
  {
    value: "generic",
    label: "通用 JSON",
    description: "向 HTTPS 地址发送统一 JSON 载荷，可选 HMAC-SHA256 签名。",
    urlPlaceholder: "https://example.com/webhook",
    secret: {
      label: "HMAC 密钥",
      placeholder: "留空则保留已保存密钥",
      extra: "配置后服务端通过 X-Spore-Signature 发送 HMAC-SHA256 签名。",
    },
    mentionOptions: [],
    alwaysFields: ["generic_signature_header"],
    textFields: {
      generic_signature_header: {
        key: "generic_signature_header",
        label: "签名请求头",
        placeholder: "X-Spore-Signature",
        extra: "留空时使用 X-Spore-Signature；仅配置 HMAC 密钥后发送签名。",
      },
    },
    defaults: { mention_mode: "none" },
  },
  {
    value: "feishu",
    label: "飞书",
    description: "发送飞书机器人消息，可选签名密钥，并支持按 open_id 或所有人提醒。",
    urlPlaceholder: "https://open.feishu.cn/open-apis/bot/v2/hook/…",
    secret: {
      label: "签名密钥",
      placeholder: "留空则保留已保存密钥",
      extra: "飞书机器人开启签名校验后填写。",
    },
    mentionOptions: [
      { value: "none", label: "不提醒" },
      { value: "users", label: "指定用户", fieldKey: "open_ids" },
      { value: "all", label: "所有人" },
    ],
    textFields: {
      open_ids: {
        key: "open_ids",
        label: "飞书 open_id",
        placeholder: "ou_xxx, ou_yyy",
        extra: "多个 open_id 使用英文逗号分隔。",
      },
    },
    defaults: { mention_mode: "none" },
  },
  {
    value: "dingtalk",
    label: "钉钉",
    description: "发送钉钉自定义机器人消息，可选加签，并支持手机号或所有人提醒。",
    urlPlaceholder: "https://oapi.dingtalk.com/robot/send?access_token=…",
    secret: {
      label: "加签密钥",
      placeholder: "留空则保留已保存密钥",
      extra: "钉钉机器人开启加签后填写。",
    },
    mentionOptions: [
      { value: "none", label: "不提醒" },
      { value: "users", label: "指定手机号", fieldKey: "mobiles" },
      { value: "all", label: "所有人" },
    ],
    textFields: {
      mobiles: {
        key: "mobiles",
        label: "提醒手机号",
        placeholder: "13800000000, 13900000000",
        extra: "多个手机号使用英文逗号分隔。",
      },
    },
    defaults: { mention_mode: "none" },
  },
  {
    value: "discord",
    label: "Discord",
    description: "发送 Discord Webhook 消息，并通过 allowed_mentions 限制用户、角色或 everyone 提醒。",
    urlPlaceholder: "https://discord.com/api/webhooks/…",
    mentionOptions: [
      { value: "none", label: "不提醒" },
      { value: "users", label: "指定用户", fieldKey: "user_ids" },
      { value: "roles", label: "指定角色", fieldKey: "role_ids" },
      { value: "all", label: "@everyone" },
    ],
    textFields: {
      user_ids: {
        key: "user_ids",
        label: "Discord 用户 ID",
        placeholder: "123456789, 987654321",
        extra: "多个用户 ID 使用英文逗号分隔。",
      },
      role_ids: {
        key: "role_ids",
        label: "Discord 角色 ID",
        placeholder: "123456789, 987654321",
        extra: "多个角色 ID 使用英文逗号分隔。",
      },
    },
    defaults: { mention_mode: "none" },
  },
];

export const WEBHOOK_FORMAT_OPTIONS = WEBHOOK_FORMATS.map(({ value, label }) => ({
  value,
  label,
}));

export function webhookFormatDescriptor(format: NotificationWebhookFormat) {
  return WEBHOOK_FORMATS.find((item) => item.value === format) ?? WEBHOOK_FORMATS[0];
}
