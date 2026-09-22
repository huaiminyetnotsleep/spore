/**
 * 运行设置页（SSR /settings 的 SPA 对应实现，可编辑部分）。
 * 按生效时机分区：基础运营（时区，保存后即时生效）、队列与媒体
 * （队列容量与媒体传输参数，重启生效）、传输并发调试；校验边界与 SSR 一致
 * （队列 1–4096、媒体 1MB–2000MB 且阈值 ≤ 上限，服务端复核）。
 * 媒体大小输入为「可选覆盖」语义：留空表示保持当前值（占位符回显当前值），
 * 不再使用视觉 required 标记；载荷仅携带明确编辑过的字段。
 * 频道加入（/join）与频道同步的配置分别在「受邀设置」「频道设置」页维护；
 * 受控重启从管理端 Header 的 Avatar 菜单触发。
 */
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";
import { Link } from "react-router-dom";

import { fetchBackupStatus, fetchSettings } from "../../api/admin";
import {
  saveSettings,
  type SettingsSaveInput,
  type TransferConfigKey,
} from "../../api/mutations";
import { fmtBytes, fmtTime } from "../../shared/format";
import { useAdminAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";

const { Text } = Typography;

interface SettingsFormValues {
  timezone?: string;
  max_links_per_message?: number;
  max_request_attempts?: number;
  backup_interval_hours?: number;
  backup_keep_count?: number;
  queue_capacity?: number;
  worker_count?: number;
  max_file_size?: string;
  max_file_unit?: "MB" | "GB";
  stream_limit?: string;
  stream_limit_unit?: "MB" | "GB";
  temp_dir_max_size?: string;
  temp_dir_max_size_unit?: "MB" | "GB";
  memory_budget?: string;
  memory_budget_unit?: "MB" | "GB";
  download_threads?: number;
  upload_threads?: number;
  download_connections?: number;
  upload_connections?: number;
}

const TRANSFER_FIELDS: Array<{
  key: TransferConfigKey;
  label: string;
  envKey: `${TransferConfigKey}_env`;
  overriddenKey: `${TransferConfigKey}_overridden`;
}> = [
  {
    key: "download_threads",
    label: "下载分片线程数",
    envKey: "download_threads_env",
    overriddenKey: "download_threads_overridden",
  },
  {
    key: "upload_threads",
    label: "上传分片线程数",
    envKey: "upload_threads_env",
    overriddenKey: "upload_threads_overridden",
  },
  {
    key: "download_connections",
    label: "下载连接并发上限",
    envKey: "download_connections_env",
    overriddenKey: "download_connections_overridden",
  },
  {
    key: "upload_connections",
    label: "上传连接并发上限",
    envKey: "upload_connections_env",
    overriddenKey: "upload_connections_overridden",
  },
];

const MEDIA_UNIT_OPTIONS = [
  { value: "MB", label: "MB" },
  { value: "GB", label: "GB" },
];

export function SettingsPage() {
  const [form] = Form.useForm<SettingsFormValues>();
  const [dirtyFields, setDirtyFields] = useState<Set<string>>(() => new Set());
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["settings"],
    queryFn: fetchSettings,
  });
  // 重启待生效提示与 SSR 一致：媒体配置差异或已确认的待导入备份
  const backup = useQuery({ queryKey: ["backup"], queryFn: fetchBackupStatus });

  // Ant Design 的 initialValues 只在首次挂载时读取；设置数据异步返回后，
  // 必须显式回填可编辑字段，避免页面显示为空而保存时误覆盖配置。
  useEffect(() => {
    if (!data) return;
    form.setFieldsValue({
      timezone: data.timezone,
      max_links_per_message: data.max_links_per_message,
      max_request_attempts: data.max_request_attempts,
      backup_interval_hours: data.backup_interval_hours,
      backup_keep_count: data.backup_keep_count,
      queue_capacity: data.queue_capacity,
      worker_count: data.worker_count,
      download_threads: data.download_threads,
      upload_threads: data.upload_threads,
      download_connections: data.download_connections,
      upload_connections: data.upload_connections,
    });
  }, [data, form]);

  const save = useAdminAction({
    action: (values: SettingsFormValues) => {
      const input: SettingsSaveInput = {};
      if (dirtyFields.has("timezone") && values.timezone !== undefined && values.timezone.trim() !== "") {
        input.timezone = values.timezone.trim();
      }
      if (dirtyFields.has("max_links_per_message") && values.max_links_per_message != null) {
        input.max_links_per_message = values.max_links_per_message;
      }
      if (dirtyFields.has("max_request_attempts") && values.max_request_attempts != null) {
        input.max_request_attempts = values.max_request_attempts;
      }
      if (dirtyFields.has("backup_interval_hours") && values.backup_interval_hours != null) {
        input.backup_interval_hours = values.backup_interval_hours;
      }
      if (dirtyFields.has("backup_keep_count") && values.backup_keep_count != null) {
        input.backup_keep_count = values.backup_keep_count;
      }
      if (dirtyFields.has("queue_capacity") && values.queue_capacity != null) {
        input.queue_capacity = values.queue_capacity;
      }
      if (dirtyFields.has("worker_count") && values.worker_count != null) {
        input.worker_count = values.worker_count;
      }
      for (const field of TRANSFER_FIELDS) {
        const value = values[field.key];
        if (dirtyFields.has(field.key) && value != null) {
          const numeric = Number(value);
          if (Number.isFinite(numeric)) input[field.key] = numeric;
        }
      }
      if (dirtyFields.has("max_file_size") && values.max_file_size?.trim()) {
        input.max_file_size = values.max_file_size.trim();
        input.max_file_unit = values.max_file_unit ?? "MB";
      }
      if (dirtyFields.has("stream_limit") && values.stream_limit?.trim()) {
        input.stream_limit = values.stream_limit.trim();
        input.stream_limit_unit = values.stream_limit_unit ?? "MB";
      }
      if (dirtyFields.has("temp_dir_max_size") && values.temp_dir_max_size?.trim()) {
        input.temp_dir_max_size = values.temp_dir_max_size.trim();
        input.temp_dir_max_size_unit = values.temp_dir_max_size_unit ?? "GB";
      }
      if (dirtyFields.has("memory_budget") && values.memory_budget?.trim()) {
        input.memory_budget = values.memory_budget.trim();
        input.memory_budget_unit = values.memory_budget_unit ?? "GB";
      }

      return saveSettings(input);
    },
    invalidate: [["settings"], ["overview"]],
    successText:
      "设置已保存。传输线程数将在下一任务（上传为下一次上传）生效；连接并发上限对新分片即时生效。其他设置按页面说明生效。",
    onDone: () => {
      setDirtyFields(new Set());
      form.setFieldsValue({
        max_file_size: "",
        stream_limit: "",
        temp_dir_max_size: "",
        memory_budget: "",
      });
    },
  });

  const clearTransferOverride = useAdminAction<
    { ok: boolean; settings: import("../../api/admin").SettingsView },
    TransferConfigKey
  >({
    action: (key) => saveSettings({ clear_transfer_overrides: [key] }),
    invalidate: [["settings"]],
    successText: "已恢复环境默认值。",
    onDone: (_data, key) => {
      setDirtyFields((current) => {
        const next = new Set(current);
        next.delete(key);
        return next;
      });
    },
  });

  // 待重启差异逐项列出（不再只取最高优先级一项）：已确认备份、媒体配置、
  // worker 数与队列容量都属于"下次重启生效"的变更。
  const restartPendingReasons: string[] = [];
  if (backup.data?.pending_state === "confirmed") {
    restartPendingReasons.push("有已确认的数据库备份等待下次启动应用");
  }
  if (data && !data.media_same) {
    restartPendingReasons.push("媒体传输配置将在下次重启后生效");
  }
  if (data && !data.worker_count_same) {
    restartPendingReasons.push(`任务并发 worker 数（${data.worker_count}）将在下次重启后生效`);
  }
  if (data && !data.queue_same) {
    restartPendingReasons.push(`全局队列容量（${data.queue_capacity}）将在下次重启后生效`);
  }

  return (
    <PageScaffold
      title="运行设置"
      description="运营时区、队列容量、媒体传输参数与传输并发调试；按生效时机分区，留空的媒体大小输入表示保持当前值。"
    >
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="middle" className="field-width-full settings-page">
          {data && !data.queue_same ? (
            <Alert
              type="info"
              showIcon
              message={`当前进程实际队列容量为 ${data.queue_runtime}，修改将在下次重启后生效。`}
            />
          ) : null}

          <Form<SettingsFormValues>
            form={form}
            layout="vertical"
            className="field-width-full settings-form"
            disabled={isPending}
            initialValues={{
              timezone: data?.timezone,
              max_links_per_message: data?.max_links_per_message,
              max_request_attempts: data?.max_request_attempts,
              backup_interval_hours: data?.backup_interval_hours,
              backup_keep_count: data?.backup_keep_count,
              queue_capacity: data?.queue_capacity,
              worker_count: data?.worker_count,
              download_threads: data?.download_threads,
              upload_threads: data?.upload_threads,
              download_connections: data?.download_connections,
              upload_connections: data?.upload_connections,
              max_file_unit: "MB",
              stream_limit_unit: "MB",
              temp_dir_max_size_unit: "GB",
              memory_budget_unit: "GB",
            }}
            onValuesChange={(changed) => {
              setDirtyFields((current) => {
                const next = new Set(current);
                Object.keys(changed).forEach((key) => next.add(key));
                return next;
              });
            }}
            onFinish={(values) => void save.run(values)}
          >
            <PageSection
              title="基础运营"
              extra={<Tag color="green">保存后即时生效</Tag>}
            >
              <div className="settings-field-grid">
                <Form.Item
                  name="timezone"
                  label="运营时区（IANA 名称）"
                  extra="日额度按该时区 00:00 切换，页面时间统一按该时区显示；IANA 名称合法性由服务端校验。"
                >
                  <Input placeholder="如 Asia/Shanghai" allowClear />
                </Form.Item>

                <Form.Item
                  name="max_links_per_message"
                  label="单次最大链接数（1–50）"
                  rules={[{ type: "integer", min: 1, max: 50, message: "单次最大链接数必须为 1–50 的整数。" }]}
                  extra="一条普通消息或 /download 命令可提交的有效链接数；超过上限时整批拒绝。保存后即时生效。"
                >
                  <InputNumber min={1} max={50} precision={0} className="field-width-160" />
                </Form.Item>

                <Form.Item
                  name="max_request_attempts"
                  label="最大尝试次数（1–10，累计含首次）"
                  rules={[{ type: "integer", min: 1, max: 10, message: "最大尝试次数必须为 1–10 的整数。" }]}
                  extra="单个请求可重试的总次数上限（首次执行计 1 次），仅约束管理端重试；保存后即时生效。已达上限的请求可在消息记录详情页重置尝试计数。"
                >
                  <InputNumber min={1} max={10} precision={0} className="field-width-160" />
                </Form.Item>

                <Form.Item
                  name="backup_interval_hours"
                  label="自动备份间隔小时（0–168，0 = 关闭）"
                  rules={[{ type: "integer", min: 0, max: 168, message: "自动备份间隔必须为 0–168 的整数小时。" }]}
                  extra="定时把数据库一致性快照写入 data/backups（VACUUM INTO）；缺省 6 小时，0 为关闭。仅含数据库——会话与配置文件的备份用备份页「全量导出」。保存后即时生效。"
                >
                  <InputNumber min={0} max={168} precision={0} className="field-width-160" />
                </Form.Item>

                <Form.Item
                  name="backup_keep_count"
                  label="备份保留份数（1–50）"
                  rules={[{ type: "integer", min: 1, max: 50, message: "备份保留份数必须为 1–50 的整数。" }]}
                  extra="自动备份按修改时间保留最近 N 份，超出自动删除最老；缺省 8 份（默认间隔下约 48 小时窗口）。保存后即时生效。"
                >
                  <InputNumber min={1} max={50} precision={0} className="field-width-160" />
                </Form.Item>

                <Form.Item
                  label="下载内存预算（64MB–8GB，留空保持当前值）"
                  extra="所有“内存管道”下载共享的常驻内存上限；预算不足的文件自动改为落临时文件边下边传，内存占用被预算封顶。调整只影响新打开的媒体。"
                >
                  <Space.Compact>
                    <Form.Item name="memory_budget" noStyle>
                      <Input placeholder={data ? `当前 ${fmtBytes(data.memory_budget_bytes)}` : "如 1"} className="field-width-200" />
                    </Form.Item>
                    <Form.Item name="memory_budget_unit" noStyle>
                      <Select options={MEDIA_UNIT_OPTIONS} className="field-width-90" />
                    </Form.Item>
                  </Space.Compact>
                </Form.Item>
              </div>
              <Text type="secondary" className="settings-note">
                重复链接相关配置（缓存频道、复用开关、重复链接检测窗口）在
                「频道设置」页的缓存频道分区维护。
              </Text>
            </PageSection>

            <PageSection
              title="队列与媒体"
              extra={<Tag color="gold">重启后生效</Tag>}
            >
              <div className="settings-field-grid">
                <Form.Item
                  name="queue_capacity"
                  label="全局队列容量"
                  rules={[{ type: "integer", min: 1, max: 4096, message: "队列容量必须为 1–4096 的整数。" }]}
                >
                  <InputNumber min={1} max={4096} className="field-width-160" />
                </Form.Item>

                <Form.Item
                  name="worker_count"
                  label="任务并发 worker 数（1–16）"
                  rules={[{ type: "integer", min: 1, max: 16, message: "任务并发 worker 数必须为 1–16 的整数。" }]}
                  extra="同时处理的任务数；调大会按并发数放大内存与 Telegram 连接占用。"
                >
                  <InputNumber min={1} max={16} precision={0} className="field-width-160" />
                </Form.Item>
                {data && !data.worker_count_same ? (
                  <span className="settings-meta">
                    当前配置：{data.worker_count}；当前进程：{data.worker_count_runtime}（重启后生效）
                  </span>
                ) : null}

                <Form.Item label="文件大小上限（1MB–2000MB，留空保持当前值）">
                  <Space.Compact>
                    <Form.Item name="max_file_size" noStyle>
                      <Input placeholder={data ? `当前 ${fmtBytes(data.max_file_size_bytes)}` : "如 50"} className="field-width-200" />
                    </Form.Item>
                    <Form.Item name="max_file_unit" noStyle>
                      <Select options={MEDIA_UNIT_OPTIONS} className="field-width-90" />
                    </Form.Item>
                  </Space.Compact>
                  {data ? (
                    <span className="settings-meta">
                      当前配置：{fmtBytes(data.max_file_size_bytes)}；当前进程：
                      {fmtBytes(data.max_file_size_runtime_bytes)}
                    </span>
                  ) : null}
                </Form.Item>

                <Form.Item label="流式传输阈值（1MB–2GB，留空保持当前值）">
                  <Space.Compact>
                    <Form.Item name="stream_limit" noStyle>
                      <Input placeholder={data ? `当前 ${fmtBytes(data.stream_limit_bytes)}` : "如 20"} className="field-width-200" />
                    </Form.Item>
                    <Form.Item name="stream_limit_unit" noStyle>
                      <Select options={MEDIA_UNIT_OPTIONS} className="field-width-90" />
                    </Form.Item>
                  </Space.Compact>
                  {data ? (
                    <span className="settings-meta">
                      当前配置：{fmtBytes(data.stream_limit_bytes)}；当前进程：
                      {fmtBytes(data.stream_limit_runtime_bytes)}
                    </span>
                  ) : null}
                </Form.Item>

                <Form.Item label="临时目录最大大小（1MB–1TB，留空保持当前值）">
                  <Space.Compact>
                    <Form.Item name="temp_dir_max_size" noStyle>
                      <Input placeholder={data ? `当前 ${fmtBytes(data.temp_dir_max_size_bytes)}` : "如 5"} className="field-width-200" />
                    </Form.Item>
                    <Form.Item name="temp_dir_max_size_unit" noStyle>
                      <Select options={MEDIA_UNIT_OPTIONS} className="field-width-90" />
                    </Form.Item>
                  </Space.Compact>
                  {data ? (
                    <span className="settings-meta">
                      当前配置：{fmtBytes(data.temp_dir_max_size_bytes)}；当前进程：
                      {fmtBytes(data.temp_dir_max_size_runtime_bytes)}
                    </span>
                  ) : null}
                </Form.Item>
              </div>
            </PageSection>

            <PageSection title="传输并发调试">
              <Text type="secondary" className="settings-note">
                线程数将在下一任务（上传为下一次上传）生效；连接并发上限对新分片即时生效。调低连接并发上限不会取消在途请求；实际文件分片并发取线程数与连接数的较小值。设置为
                1 可做单线程/单连接基线。
              </Text>
              <div className="settings-field-grid settings-margin-top-12">
                {TRANSFER_FIELDS.map((field) => {
                  const overridden = data?.[field.overriddenKey] ?? false;
                  return (
                    <Form.Item
                      key={field.key}
                      name={field.key}
                      label={field.label}
                      rules={[
                        {
                          validator: async (_rule, value) => {
                            if (value == null || value === "") return;
                            const numeric = Number(value);
                            if (!Number.isInteger(numeric) || numeric < 1 || numeric > 16) {
                              throw new Error("传输并发值必须为 1–16 的整数。");
                            }
                          },
                        },
                      ]}
                      extra={
                        data ? (
                          <span className="settings-meta">
                            当前生效：{data[field.key]} · .env 默认：{data[field.envKey]}
                            <Tag color={overridden ? "blue" : "default"}>
                              {overridden ? "数据库覆盖" : "环境默认"}
                            </Tag>
                          </span>
                        ) : undefined
                      }
                    >
                      <Space.Compact>
                        <InputNumber aria-label={field.label} min={1} max={16} precision={0} className="field-width-160" />
                        <Button
                          disabled={!overridden}
                          htmlType="button"
                          loading={clearTransferOverride.pending}
                          onClick={() => void clearTransferOverride.run(field.key)}
                        >
                          恢复环境默认
                        </Button>
                      </Space.Compact>
                    </Form.Item>
                  );
                })}
              </div>
            </PageSection>

            <FormActions>
              <Button type="primary" htmlType="submit" loading={save.pending}>
                保存设置
              </Button>
            </FormActions>
            {/* 服务端 400/409 等受控文案统一经 useAdminAction 的 message.error 提示 */}
          </Form>

          <div className="settings-status-grid">
            <PageSection title="生效状态">
              <Space direction="vertical" size="small" className="field-width-full">
                <Text>
                  媒体传输配置：
                  {data?.media_same ? (
                    <Tag color="green">与当前进程一致</Tag>
                  ) : (
                    <Tag color="gold">待重启生效</Tag>
                  )}
                </Text>
                <Text>
                  任务并发 worker 数：
                  {data?.worker_count_same ? (
                    <Tag color="green">与当前进程一致</Tag>
                  ) : (
                    <Tag color="gold">待重启生效</Tag>
                  )}
                </Text>
                <Text>
                  全局队列容量：
                  {data?.queue_same ? (
                    <Tag color="green">与当前进程一致</Tag>
                  ) : (
                    <Tag color="gold">待重启生效</Tag>
                  )}
                </Text>
                <Text type="secondary">最近备份：{fmtTime(data?.last_backup_at)}。</Text>
                <Text type="secondary">
                  时区保存后即时生效；重复链接相关配置在频道设置页即时生效；队列容量、worker
                  数与媒体传输参数在下次重启后生效，运行中的任务不受影响。
                </Text>
              </Space>
            </PageSection>

            {/* 受控重启（与 SSR /settings 页面同一归属）：入口保持在 Header 头像菜单 */}
            <PageSection title="受控重启">
              <Space direction="vertical" size="small" className="field-width-full">
                {restartPendingReasons.length > 0 ? (
                  <Alert
                    type="info"
                    showIcon
                    message={`检测到 ${restartPendingReasons.length} 项待重启变更`}
                    description={
                      <ul className="settings-restart-list">
                        {restartPendingReasons.map((reason) => (
                          <li key={reason}>{reason}</li>
                        ))}
                      </ul>
                    }
                    action={
                      backup.data?.pending_state === "confirmed" ? (
                        <Link to="/backup">查看数据备份</Link>
                      ) : undefined
                    }
                  />
                ) : (
                  <Text type="secondary">当前没有检测到待生效变更。</Text>
                )}
                <Text type="secondary">
                  优雅重启入口：右上角头像菜单 →「优雅重启」。重启只触发当前进程的
                  SIGTERM，不执行 Shell；使用 Docker Compose
                  等监管机制时会自动拉起，直接本地运行需手动重新启动。
                </Text>
              </Space>
            </PageSection>
          </div>
        </Space>
      </PageQueryState>
    </PageScaffold>
  );
}
