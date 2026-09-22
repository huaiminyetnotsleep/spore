/**
 * 备份与恢复管理页。
 * 1. 数据备份与导出（Export Center）：
 *    - 一键全量备份（数据库 + 所有 JSON 打包）
 *    - 一键所有 JSON 配置文件打包
 *    - 单项数据库备份导出
 *    - 单个 JSON 配置文件分类选择导出（Select 分组选择器 + 表格逐行导出按钮）
 *    - 数据资产明细清单（按分类展示，每项均可独立导出）
 * 2. 定时备份与云端同步（Cloudflare R2）：
 *    - 间隔/份数配置（从设置页挪入，单一入口）
 *    - R2 连接四项配置（掩码回显，凭据只存本机 data/r2-backup.json）
 *    - 连通性测试与最近上传状态
 * 3. 数据恢复与导入（Import Center）：
 *    - 业务数据库恢复（.db 校验、待确认状态与原子替换）
 *    - JSON 配置文件恢复（单文件按目标覆盖、所有 JSON 压缩包批量还原，附带平滑重启入口）
 *    - 云盘加密配置恢复与回滚（密码解密、候选确认与配置回滚）
 */
import { useQuery } from "@tanstack/react-query";
import {
  CloudOutlined,
  DatabaseOutlined,
  DownloadOutlined,
  FileTextOutlined,
  FileZipOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Row,
  Select,
  Space,
  Switch,
  Tabs,
  Tag,
  Typography,
  Upload,
} from "antd";
import { useEffect, useMemo, useState } from "react";

import {
  fetchBackupSchedule,
  fetchBackupStatus,
  fetchCloudDriveBackupStatus,
  type JSONFileInfo,
} from "../../api/admin";
import {
  cancelCloudDriveBackupPending,
  confirmBackupImport,
  confirmCloudDriveBackupImport,
  exportBackup,
  exportBackupAllJSON,
  exportBackupFull,
  exportBackupJSON,
  exportCloudDriveBackup,
  importCloudDriveBackup,
  restartServer,
  rollbackCloudDriveBackup,
  saveBackupSchedule,
  testBackupR2Connection,
  uploadBackup,
  uploadBackupAllJSON,
  uploadBackupJSON,
} from "../../api/mutations";
import { fmtBytes, fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FormActions } from "../shared/FormActions";
import { FormModal } from "../shared/FormModal";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";

const { Paragraph, Text } = Typography;

/** SSR data-confirm 同款文案：确认导入是整库替换的最后一步。 */
export const CONFIRM_IMPORT_TEXT =
  "确认整库替换业务数据库？当前用户、请求、用量和审计将被候选备份替换，安全设置会保留，操作需要下次重启生效。";

/** SSR data-confirm 同款文案：导出可能耗时数秒。 */
const CONFIRM_EXPORT_TEXT = "确定导出数据库备份？大库可能需要数秒。";

const CONFIRM_CLOUD_DRIVE_IMPORT_TEXT =
  "确认整体替换云盘配置？当前所有目的地和凭据将被候选备份替换，并立即生效。";

const CONFIRM_CLOUD_DRIVE_ROLLBACK_TEXT =
  "确认恢复上一个云盘配置？当前目的地和凭据将被上一个备份替换，并立即生效。";

const CONFIRM_RESTART_TEXT =
  "确认立即重启 Spore 服务？服务将在数秒内平滑重启以重新加载会话与配置。";

interface ExportCloudBackupFormValues {
  password: string;
  password_confirmation: string;
}

/** 定时备份卡片表单值：密钥字段展示掩码（服务端把掩码/空串视为沿用）。 */
interface ScheduleFormValues {
  interval_hours: number;
  keep_count: number;
  enabled: boolean;
  account_id: string;
  access_key_id: string;
  secret_access_key: string;
  bucket: string;
}

export function BackupPage() {
  const [selectedDbFile, setSelectedDbFile] = useState<File | null>(null);

  // 定时备份 + R2 上云配置
  const [scheduleForm] = Form.useForm<ScheduleFormValues>();

  // 单 JSON 导入状态
  const [selectedSingleJson, setSelectedSingleJson] = useState<File | null>(null);
  const [singleJsonTarget, setSingleJsonTarget] = useState<string>("");

  // 批量 JSON 导入状态
  const [selectedAllJsonZip, setSelectedAllJsonZip] = useState<File | null>(null);

  // 云盘备份状态
  const [cloudExportModalOpen, setCloudExportModalOpen] = useState(false);
  const [cloudExportForm] = Form.useForm<ExportCloudBackupFormValues>();
  const [selectedCloudBackup, setSelectedCloudBackup] = useState<File | null>(null);
  const [cloudImportPassword, setCloudImportPassword] = useState("");

  // 重启提示标记
  const [needRestartNotice, setNeedRestartNotice] = useState<string | null>(null);

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["backup"],
    queryFn: fetchBackupStatus,
  });

  const cloudBackupStatus = useQuery({
    queryKey: ["cloud-drive-backup"],
    queryFn: fetchCloudDriveBackupStatus,
  });

  // ---- 定时备份 + R2 上云 ----
  const scheduleQuery = useQuery({
    queryKey: ["backup-schedule"],
    queryFn: fetchBackupSchedule,
  });

  // 异步数据到达后回填表单（initialValues 仅在挂载时读取一次）
  useEffect(() => {
    if (!scheduleQuery.data) return;
    const { r2 } = scheduleQuery.data;
    scheduleForm.setFieldsValue({
      interval_hours: scheduleQuery.data.interval_hours,
      keep_count: scheduleQuery.data.keep_count,
      enabled: r2.enabled,
      account_id: r2.account_id,
      access_key_id: r2.access_key_id,
      secret_access_key: r2.secret_access_key,
      bucket: r2.bucket,
    });
  }, [scheduleQuery.data, scheduleForm]);

  const saveScheduleAction = useAdminAction({
    action: (values: ScheduleFormValues) =>
      saveBackupSchedule({
        interval_hours: values.interval_hours,
        keep_count: values.keep_count,
        r2: {
          enabled: values.enabled,
          account_id: values.account_id,
          access_key_id: values.access_key_id,
          secret_access_key: values.secret_access_key,
          bucket: values.bucket,
        },
      }),
    invalidate: [["backup-schedule"], ["settings"], ["overview"]],
    successText: "定时备份配置已保存，即时生效（下一轮定时检查按新配置执行）。",
  });

  const testR2Action = useAdminAction({
    action: () => testBackupR2Connection(),
    invalidate: [["backup-schedule"]],
    successText: (result) =>
      result.connected
        ? "连接成功：R2 存储桶可访问。"
        : { type: "warning", text: result.message },
  });

  // ---- 导出动作 ----
  const exportDbAction = useAdminAction({
    action: () => exportBackup(),
    invalidate: [["backup"]],
    successText: "数据库备份已导出，请查收浏览器下载。",
  });

  const exportAllJsonAction = useAdminAction({
    action: () => exportBackupAllJSON(),
    invalidate: [["backup"]],
    successText: "所有 JSON 配置文件打包已导出，请查收浏览器下载。",
  });

  const exportFullAction = useAdminAction({
    action: () => exportBackupFull(),
    invalidate: [["backup"]],
    successText: "全量备份包（数据库 + 所有 JSON）已导出，请查收浏览器下载。",
  });

  const exportSingleJsonAction = useAdminAction({
    action: (filename: string) => exportBackupJSON(filename),
    invalidate: [["backup"]],
    successText: (filename) => `配置文件 ${filename} 已导出。`,
  });

  // ---- 数据库导入动作 ----
  const uploadDb = useAdminAction({
    action: (file: File) => uploadBackup(file),
    invalidate: [["backup"]],
    successText: (result) => result.message,
    onDone: () => setSelectedDbFile(null),
  });

  const confirmDbImport = useAdminAction({
    action: () => confirmBackupImport(),
    invalidate: [["backup"]],
    successText: (result) => result.message,
  });

  // ---- JSON 导入动作 ----
  const uploadSingleJson = useAdminAction({
    action: ({ file, targetName }: { file: File; targetName?: string }) =>
      uploadBackupJSON(file, targetName),
    invalidate: [["backup"]],
    successText: (result) => {
      setNeedRestartNotice(result.message);
      return result.message;
    },
    onDone: () => {
      setSelectedSingleJson(null);
      setSingleJsonTarget("");
    },
  });

  const uploadAllJson = useAdminAction({
    action: (file: File) => uploadBackupAllJSON(file),
    invalidate: [["backup"]],
    successText: (result) => {
      setNeedRestartNotice(result.message);
      return result.message;
    },
    onDone: () => setSelectedAllJsonZip(null),
  });

  // ---- 云盘备份动作 ----
  const exportCloudAction = useAdminAction<{ exported: true }, ExportCloudBackupFormValues>({
    action: async (values) => {
      await exportCloudDriveBackup(values.password);
      return { exported: true };
    },
    successText: "云盘配置加密备份已导出，请查收浏览器下载。",
  });

  const importCloudAction = useAdminAction({
    action: ({ backup, password }: { backup: File; password: string }) =>
      importCloudDriveBackup(backup, password),
    invalidate: [["cloud-drive-backup"]],
    successText: "云盘备份已验证，尚未应用。",
    onDone: () => {
      setSelectedCloudBackup(null);
      setCloudImportPassword("");
    },
  });

  const confirmCloudImport = useAdminAction({
    action: () => confirmCloudDriveBackupImport(),
    invalidate: [["cloud-drive"], ["cloud-drive-backup"]],
    successText: "云盘配置已从候选备份恢复并立即生效。",
  });

  const cancelCloudPending = useAdminAction({
    action: () => cancelCloudDriveBackupPending(),
    invalidate: [["cloud-drive-backup"]],
    successText: "待确认的云盘配置候选已取消。",
  });

  const rollbackCloud = useAdminAction({
    action: () => rollbackCloudDriveBackup(),
    invalidate: [["cloud-drive"], ["cloud-drive-backup"]],
    successText: "已恢复上一个云盘配置并立即生效。",
  });

  // ---- 重启动作 ----
  const restartAction = useAdminAction({
    action: () => restartServer(),
    successText: "已发送重启指令，服务正在重启…",
    onDone: () => setNeedRestartNotice(null),
  });

  const confirm = useConfirmAction();

  // 资产明细列表
  const assetRows = useMemo(() => {
    return [
      {
        key: "db",
        name: "spore.db",
        category: "业务数据库",
        categoryTag: <Tag color="blue">数据库</Tag>,
        isDb: true,
        description: "SQLite 业务主数据库（用户、用量、请求、审计等）",
        size: data ? data.db_size_bytes : 0,
        modTime: data ? data.last_backup_at : 0,
      },
      ...(data?.json_files ?? []).map((jf: JSONFileInfo) => {
        let cat = "其他配置";
        let color = "default";
        if (jf.name === "session.json" || jf.name === "peers.json") {
          cat = "Telegram MTProto";
          color = "cyan";
        } else if (jf.name === "bots.json" || jf.name.startsWith("bot-session")) {
          cat = "机器人配置";
          color = "purple";
        } else if (jf.name === "cloud-drive.json") {
          cat = "云盘存储";
          color = "orange";
        }
        return {
          key: jf.name,
          name: jf.name,
          category: cat,
          categoryTag: <Tag color={color}>{cat}</Tag>,
          isDb: false,
          description: jf.description,
          size: jf.size_bytes,
          modTime: jf.mod_time,
        };
      }),
    ];
  }, [data]);

  // 单个 JSON 导出选择器（按分类分组）
  const [selectedExportJson, setSelectedExportJson] = useState<string | null>(null);

  const singleJsonSelectOptions = useMemo(() => {
    const files = data?.json_files ?? [];
    const mtproto = files.filter(
      (f) => f.name === "session.json" || f.name === "peers.json",
    );
    const bots = files.filter(
      (f) => f.name === "bots.json" || f.name.startsWith("bot-session"),
    );
    const storage = files.filter((f) => f.name === "cloud-drive.json");
    const others = files.filter(
      (f) =>
        f.name !== "session.json" &&
        f.name !== "peers.json" &&
        f.name !== "bots.json" &&
        !f.name.startsWith("bot-session") &&
        f.name !== "cloud-drive.json",
    );

    const groups: { label: string; options: { value: string; label: string }[] }[] = [];

    if (mtproto.length > 0) {
      groups.push({
        label: "Telegram MTProto 凭据",
        options: mtproto.map((f) => ({
          value: f.name,
          label: `${f.name}（${f.description}）`,
        })),
      });
    }

    if (bots.length > 0) {
      groups.push({
        label: "机器人配置与会话",
        options: bots.map((f) => ({
          value: f.name,
          label: `${f.name}（${f.description}）`,
        })),
      });
    }

    if (storage.length > 0) {
      groups.push({
        label: "云盘存储与挂载",
        options: storage.map((f) => ({
          value: f.name,
          label: `${f.name}（${f.description}）`,
        })),
      });
    }

    if (others.length > 0) {
      groups.push({
        label: "其他配置文件",
        options: others.map((f) => ({
          value: f.name,
          label: f.name,
        })),
      });
    }

    // 默认选中第一个文件
    if (!selectedExportJson && files.length > 0) {
      setSelectedExportJson(files[0].name);
    }

    return groups;
  }, [data?.json_files, selectedExportJson]);

  const pendingCloudBackup = cloudBackupStatus.data?.pending
    ? cloudBackupStatus.data
    : null;

  return (
    <PageScaffold
      title="数据备份"
      description="统一管理系统业务数据库、JSON 配置文件及云盘加密凭据；支持单项独立下载与一键全量备份打包。"
    >
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="large" className="field-width-full">
          {needRestartNotice ? (
            <Alert
              type="info"
              showIcon
              message={needRestartNotice}
              action={
                <Button
                  type="primary"
                  icon={<ReloadOutlined />}
                  loading={restartAction.pending}
                  onClick={() =>
                    confirm({
                      intent: "danger",
                      title: "确认重启服务",
                      content: CONFIRM_RESTART_TEXT,
                      action: () => restartAction.run(undefined),
                    })
                  }
                >
                  立即重启服务
                </Button>
              }
            />
          ) : null}

          {data?.pending ? (
            <Alert
              type={data.pending_state === "confirmed" ? "warning" : "info"}
              showIcon
              message={`待导入状态：${data.pending_state}${data.pending_sha256 ? `，摘要 ${data.pending_sha256}` : ""}。确认后下一次启动才会应用。`}
              action={
                data.pending_state === "待确认" ? (
                  <Button
                    danger
                    type="primary"
                    loading={confirmDbImport.pending}
                    onClick={() =>
                      confirm({
                        intent: "danger",
                        title: "确认导入备份（整库替换）",
                        content: CONFIRM_IMPORT_TEXT,
                        action: () => confirmDbImport.run(undefined),
                      })
                    }
                  >
                    确认导入（下次启动应用）
                  </Button>
                ) : undefined
              }
            />
          ) : null}

          {/* ==================== 区域一：数据备份与导出 ==================== */}
          <PageSection title="数据备份与导出">
            <Space direction="vertical" size="middle" className="field-width-full">
              {/* 四张操作卡片：全量导出、所有 JSON、数据库、多级单项 JSON 导出 */}
              <Row gutter={[16, 16]}>
                <Col xs={24} sm={12} lg={6}>
                  <Card size="small" hoverable className="backup-card">
                    <Space direction="vertical" size={8} className="field-width-full">
                      <Space>
                        <FileZipOutlined className="backup-card__icon--blue" />
                        <Text strong>全量完整备份</Text>
                      </Space>
                      <Paragraph type="secondary" className="backup-card__desc">
                        打包一致性数据库快照与全部 JSON 配置文件为 ZIP。
                      </Paragraph>
                      <Button
                        type="primary"
                        block
                        icon={<FileZipOutlined />}
                        loading={exportFullAction.pending}
                        onClick={() =>
                          confirm({
                            intent: "default",
                            title: "确认导出全量备份",
                            content: "确定导出完整备份？将把数据库一致性快照和所有 JSON 配置文件打包为一个 ZIP 文件。",
                            action: () => exportFullAction.run(undefined),
                          })
                        }
                      >
                        一键导出全量包
                      </Button>
                    </Space>
                  </Card>
                </Col>

                <Col xs={24} sm={12} lg={6}>
                  <Card size="small" hoverable className="backup-card">
                    <Space direction="vertical" size={8} className="field-width-full">
                      <Space>
                        <FileZipOutlined className="backup-card__icon--purple" />
                        <Text strong>所有 JSON 归档</Text>
                      </Space>
                      <Paragraph type="secondary" className="backup-card__desc">
                        打包 data/ 下所有 JSON 文件并附带校验清单。
                      </Paragraph>
                      <Button
                        block
                        icon={<FileZipOutlined />}
                        loading={exportAllJsonAction.pending}
                        onClick={() => exportAllJsonAction.run(undefined)}
                      >
                        导出所有 JSON
                      </Button>
                    </Space>
                  </Card>
                </Col>

                <Col xs={24} sm={12} lg={6}>
                  <Card size="small" hoverable className="backup-card">
                    <Space direction="vertical" size={8} className="field-width-full">
                      <Space>
                        <DatabaseOutlined className="backup-card__icon--cyan" />
                        <Text strong>业务主数据库</Text>
                      </Space>
                      <Paragraph type="secondary" className="backup-card__desc">
                        导出单一 SQLite 快照文件（spore-backup-*.db）。
                      </Paragraph>
                      <Button
                        block
                        icon={<DownloadOutlined />}
                        loading={exportDbAction.pending}
                        onClick={() =>
                          confirm({
                            intent: "default",
                            title: "确认导出数据库备份",
                            content: CONFIRM_EXPORT_TEXT,
                            action: () => exportDbAction.run(undefined),
                          })
                        }
                      >
                        仅导出数据库
                      </Button>
                    </Space>
                  </Card>
                </Col>

                <Col xs={24} sm={12} lg={6}>
                  <Card size="small" hoverable className="backup-card">
                    <Space direction="vertical" size={8} className="field-width-full">
                      <Space>
                        <FileTextOutlined className="backup-card__icon--orange" />
                        <Text strong>单个 JSON 导出</Text>
                      </Space>
                      <Paragraph type="secondary" className="backup-card__desc">
                        从下拉列表中选择目标配置文件，点击按钮下载。
                      </Paragraph>
                      <Select
                        className="field-width-full"
                        placeholder="选择要导出的 JSON 文件"
                        value={selectedExportJson}
                        onChange={(val) => setSelectedExportJson(val)}
                        options={singleJsonSelectOptions}
                        disabled={(data?.json_files ?? []).length === 0}
                      />
                      <Button
                        block
                        icon={<DownloadOutlined />}
                        loading={exportSingleJsonAction.pending}
                        disabled={!selectedExportJson}
                        onClick={() => {
                          if (selectedExportJson) {
                            void exportSingleJsonAction.run(selectedExportJson);
                          }
                        }}
                      >
                        导出选中配置
                      </Button>
                    </Space>
                  </Card>
                </Col>
              </Row>

              {/* 数据与配置文件清单表格 */}
              <DataTable
                density="compact"
                rowKey="name"
                pagination={false}
                loading={isPending}
                columns={[
                  {
                    title: "文件名",
                    dataIndex: "name",
                    key: "name",
                    render: (val: string, r: (typeof assetRows)[0]) => (
                      <Space>
                        {r.isDb ? <DatabaseOutlined /> : <FileTextOutlined />}
                        <Text strong={r.isDb}>{val}</Text>
                      </Space>
                    ),
                  },
                  {
                    title: "分类",
                    dataIndex: "categoryTag",
                    key: "categoryTag",
                    width: 140,
                  },
                  {
                    title: "配置用途与说明",
                    dataIndex: "description",
                    key: "description",
                  },
                  {
                    title: "文件大小",
                    dataIndex: "size",
                    key: "size",
                    render: (size: number) => (size > 0 ? fmtBytes(size) : "—"),
                  },
                  {
                    title: "最近修改/备份",
                    dataIndex: "modTime",
                    key: "modTime",
                    render: (modTime: number) => (modTime > 0 ? fmtTime(modTime) : "—"),
                  },
                  {
                    title: "操作",
                    key: "action",
                    render: (_: unknown, r: (typeof assetRows)[0]) => (
                      <Button
                        size="small"
                        icon={<DownloadOutlined />}
                        loading={
                          r.isDb
                            ? exportDbAction.pending
                            : exportSingleJsonAction.pending
                        }
                        onClick={() => {
                          if (r.isDb) {
                            confirm({
                              intent: "default",
                              title: "确认导出数据库备份",
                              content: CONFIRM_EXPORT_TEXT,
                              action: () => exportDbAction.run(undefined),
                            });
                          } else {
                            void exportSingleJsonAction.run(r.name);
                          }
                        }}
                      >
                        导出
                      </Button>
                    ),
                  },
                ]}
                dataSource={assetRows}
              />
              <Paragraph type="secondary" className="layout-margin-top-8 layout-margin-bottom-0">
                主数据库路径：{data ? data.db_path : "—"}，最近备份：{data ? fmtTime(data.last_backup_at) : "—"}
              </Paragraph>
            </Space>
          </PageSection>

          {/* ==================== 区域二：定时备份与云端同步 ==================== */}
          <PageSection title="定时备份与云端同步（Cloudflare R2）">
            <Space direction="vertical" size="middle" className="field-width-full">
              <Paragraph type="secondary" className="layout-margin-top-0 layout-margin-bottom-0">
                按间隔自动生成数据库一致性快照写入 data/backups 并保留最近 N 份；开启 R2
                后，同一份快照会打成全量 ZIP（含 JSON 配置）直传 Cloudflare R2
                异地保存，远端同样按份数轮转。间隔 0 为关闭。R2 凭据只存本机
                data/r2-backup.json，不进数据库、不进任何备份件；上传失败会进入事件中心告警。
                四项配置的获取步骤见部署文档《Cloudflare R2 备份指南》。
              </Paragraph>

              {scheduleQuery.data?.r2.last_upload_error ? (
                <Alert
                  type="warning"
                  showIcon
                  message={`最近一次 R2 上传未成功：${scheduleQuery.data.r2.last_upload_error}`}
                  description={`最近上传尝试：${fmtTime(scheduleQuery.data.r2.last_upload_at)}。本地快照不受影响，下一轮定时备份会重试上传。`}
                />
              ) : null}

              <Form<ScheduleFormValues>
                form={scheduleForm}
                layout="vertical"
                className="field-width-full"
                disabled={scheduleQuery.isPending}
                onFinish={(values) => void saveScheduleAction.run(values)}
              >
                <div className="settings-field-grid">
                  <Form.Item
                    name="interval_hours"
                    label="备份间隔小时（0–168，0 = 关闭）"
                    rules={[{ type: "integer", min: 0, max: 168, message: "备份间隔必须为 0–168 的整数小时。" }]}
                    extra="定时生成快照的周期；缺省 6 小时。保存后即时生效。"
                  >
                    <InputNumber min={0} max={168} precision={0} className="field-width-160" />
                  </Form.Item>

                  <Form.Item
                    name="keep_count"
                    label="保留份数（1–50）"
                    rules={[{ type: "integer", min: 1, max: 50, message: "保留份数必须为 1–50 的整数。" }]}
                    extra="本地与 R2 各保留最近 N 份；缺省 8 份（默认间隔下约 48 小时窗口）。"
                  >
                    <InputNumber min={1} max={50} precision={0} className="field-width-160" />
                  </Form.Item>

                  <Form.Item
                    name="enabled"
                    label="上传到 Cloudflare R2"
                    valuePropName="checked"
                    extra="开启前需填齐右侧四项连接配置；关闭只停止上云，本地定时备份照常。"
                  >
                    <Switch />
                  </Form.Item>

                  <Form.Item
                    name="account_id"
                    label="Account ID（32 位十六进制）"
                    extra="Cloudflare 控制台右侧栏「Account ID」，粘贴后自动拼出 S3 端点。"
                  >
                    <Input placeholder="如 0123456789abcdef0123456789abcdef" allowClear />
                  </Form.Item>

                  <Form.Item
                    name="access_key_id"
                    label="Access Key ID"
                    extra="R2 API Token 生成；显示为掩码或留空表示沿用已保存值。"
                  >
                    <Input autoComplete="off" allowClear />
                  </Form.Item>

                  <Form.Item
                    name="secret_access_key"
                    label="Secret Access Key"
                    extra="与 Access Key 配对；显示为掩码或留空表示沿用已保存值。"
                  >
                    <Input.Password autoComplete="new-password" />
                  </Form.Item>

                  <Form.Item
                    name="bucket"
                    label="Bucket（专用桶，如 spore-backup）"
                    extra="建议为备份单独建桶；不开启公开访问与版本化。"
                  >
                    <Input placeholder="如 spore-backup" allowClear />
                  </Form.Item>
                </div>
                <FormActions>
                  <Button
                    icon={<SafetyCertificateOutlined />}
                    loading={testR2Action.pending}
                    disabled={!scheduleQuery.data}
                    onClick={() => void testR2Action.run(undefined)}
                  >
                    测试连接
                  </Button>
                  <Button
                    type="primary"
                    htmlType="submit"
                    loading={saveScheduleAction.pending}
                    disabled={!scheduleQuery.data}
                  >
                    保存配置
                  </Button>
                </FormActions>
              </Form>

              <Space wrap size={8}>
                <Text type="secondary">
                  最近本地快照：{scheduleQuery.data ? fmtTime(scheduleQuery.data.last_backup_at) : "—"}
                </Text>
                <Text type="secondary">
                  最近 R2 上传：{scheduleQuery.data ? fmtTime(scheduleQuery.data.r2.last_upload_at) : "—"}
                </Text>
                {scheduleQuery.data?.r2.endpoint ? (
                  <Text type="secondary">端点：{scheduleQuery.data.r2.endpoint}</Text>
                ) : null}
              </Space>
            </Space>
          </PageSection>

          {/* ==================== 区域三：数据恢复与导入 ==================== */}
          <PageSection title="数据恢复与导入">
            <Tabs
              defaultActiveKey="json"
              type="card"
              items={[
                {
                  key: "json",
                  label: (
                    <Space>
                      <FileTextOutlined />
                      <span>JSON 配置文件恢复</span>
                    </Space>
                  ),
                  children: (
                    <Tabs
                      defaultActiveKey="single"
                      type="line"
                      items={[
                        {
                          key: "single",
                          label: "单个 JSON 文件覆盖导入",
                          children: (
                            <Space direction="vertical" size="small" className="field-width-full">
                              <Paragraph type="secondary" className="layout-margin-top-0 layout-margin-bottom-0">
                                选择单个 .json 配置文件上传，将直接覆盖 data/ 目录下的对应配置文件。支持覆盖 session.json、peers.json、cloud-drive.json 等。
                              </Paragraph>
                              <Space wrap>
                                <Upload
                                  name="json_file"
                                  accept=".json,application/json"
                                  showUploadList={false}
                                  maxCount={1}
                                  disabled={uploadSingleJson.pending}
                                  beforeUpload={(file) => {
                                    setSelectedSingleJson(file);
                                    if (!singleJsonTarget) {
                                      setSingleJsonTarget(file.name);
                                    }
                                    return false;
                                  }}
                                >
                                  <Button icon={<UploadOutlined />}>选择 JSON 文件</Button>
                                </Upload>
                                <Select
                                  className="field-width-240"
                                  placeholder="选择或输入目标文件名"
                                  value={singleJsonTarget || undefined}
                                  onChange={(val) => setSingleJsonTarget(val)}
                                  options={[
                                    { value: "session.json", label: "session.json (用户会话)" },
                                    { value: "peers.json", label: "peers.json (实体缓存)" },
                                    { value: "cloud-drive.json", label: "cloud-drive.json (云盘配置)" },
                                    { value: "bots.json", label: "bots.json (机器人列表)" },
                                    { value: "bot-session.json", label: "bot-session.json (机器人会话)" },
                                  ]}
                                  disabled={uploadSingleJson.pending}
                                />
                                <Input
                                  placeholder="或输入自定义文件名 (如 custom.json)"
                                  value={singleJsonTarget}
                                  onChange={(e) => setSingleJsonTarget(e.target.value)}
                                  className="field-width-200"
                                  disabled={uploadSingleJson.pending}
                                />
                                <Button
                                  type="primary"
                                  disabled={!selectedSingleJson}
                                  loading={uploadSingleJson.pending}
                                  onClick={() => {
                                    if (selectedSingleJson) {
                                      void uploadSingleJson.run({
                                        file: selectedSingleJson,
                                        targetName: singleJsonTarget || undefined,
                                      });
                                    }
                                  }}
                                >
                                  上传并覆盖
                                </Button>
                              </Space>
                              {selectedSingleJson ? (
                                <Space wrap size={8}>
                                  <Text type="secondary">
                                    已选择：{selectedSingleJson.name}（{fmtBytes(selectedSingleJson.size)}）
                                  </Text>
                                  <Button
                                    size="small"
                                    disabled={uploadSingleJson.pending}
                                    onClick={() => {
                                      setSelectedSingleJson(null);
                                      setSingleJsonTarget("");
                                    }}
                                  >
                                    清除选择
                                  </Button>
                                </Space>
                              ) : null}
                            </Space>
                          ),
                        },
                        {
                          key: "all",
                          label: "所有 JSON 压缩包批量导入",
                          children: (
                            <Space direction="vertical" size="small" className="field-width-full">
                              <Paragraph type="secondary" className="layout-margin-top-0 layout-margin-bottom-0">
                                上传之前导出的 JSON ZIP 归档包，系统会全面校验其中的 JSON 文件并批量还原至 data/ 目录。
                              </Paragraph>
                              <Space wrap>
                                <Upload
                                  name="all_json_zip"
                                  accept=".zip,application/zip"
                                  showUploadList={false}
                                  maxCount={1}
                                  disabled={uploadAllJson.pending}
                                  beforeUpload={(file) => {
                                    setSelectedAllJsonZip(file);
                                    return false;
                                  }}
                                >
                                  <Button icon={<UploadOutlined />}>选择 JSON ZIP 备份包</Button>
                                </Upload>
                                <Button
                                  type="primary"
                                  disabled={!selectedAllJsonZip}
                                  loading={uploadAllJson.pending}
                                  onClick={() => {
                                    if (selectedAllJsonZip) {
                                      void uploadAllJson.run(selectedAllJsonZip);
                                    }
                                  }}
                                >
                                  批量导入解压
                                </Button>
                              </Space>
                              {selectedAllJsonZip ? (
                                <Space wrap size={8}>
                                  <Text type="secondary">
                                    已选择：{selectedAllJsonZip.name}（{fmtBytes(selectedAllJsonZip.size)}）
                                  </Text>
                                  <Button
                                    size="small"
                                    disabled={uploadAllJson.pending}
                                    onClick={() => setSelectedAllJsonZip(null)}
                                  >
                                    清除选择
                                  </Button>
                                </Space>
                              ) : null}
                            </Space>
                          ),
                        },
                      ]}
                    />
                  ),
                },
                {
                  key: "db",
                  label: (
                    <Space>
                      <DatabaseOutlined />
                      <span>业务数据库整库恢复</span>
                    </Space>
                  ),
                  children: (
                    <Space direction="vertical" size="small" className="field-width-full">
                      <Paragraph type="secondary" className="layout-margin-top-0 layout-margin-bottom-0">
                        仅接受本管理端导出的 SQLite .db 文件。上传会先校验，不会立即改变当前数据库。
                      </Paragraph>
                      <Space wrap>
                        <Upload
                          name="backup"
                          accept=".db,application/octet-stream"
                          showUploadList={false}
                          maxCount={1}
                          disabled={uploadDb.pending}
                          beforeUpload={(file) => {
                            setSelectedDbFile(file);
                            return false;
                          }}
                        >
                          <Button icon={<UploadOutlined />}>选择 .db 文件</Button>
                        </Upload>
                        <Button
                          type="primary"
                          disabled={!selectedDbFile}
                          loading={uploadDb.pending}
                          onClick={() => selectedDbFile && void uploadDb.run(selectedDbFile)}
                        >
                          上传并校验
                        </Button>
                      </Space>
                      {selectedDbFile ? (
                        <Space wrap size={8}>
                          <Text type="secondary">
                            已选择：{selectedDbFile.name}（{fmtBytes(selectedDbFile.size)}）
                          </Text>
                          <Button
                            size="small"
                            disabled={uploadDb.pending}
                            onClick={() => setSelectedDbFile(null)}
                          >
                            清除选择
                          </Button>
                        </Space>
                      ) : null}

                      {data?.pending ? (
                        <Alert
                          className="layout-margin-top-16"
                          type={data.pending_state === "confirmed" ? "warning" : "info"}
                          showIcon
                          message={`待导入状态：${data.pending_state}${data.pending_sha256 ? `，摘要 ${data.pending_sha256}` : ""}。确认后下一次启动才会应用。`}
                          action={
                            data.pending_state === "待确认" ? (
                              <Button
                                danger
                                type="primary"
                                loading={confirmDbImport.pending}
                                onClick={() =>
                                  confirm({
                                    intent: "danger",
                                    title: "确认导入备份（整库替换）",
                                    content: CONFIRM_IMPORT_TEXT,
                                    action: () => confirmDbImport.run(undefined),
                                  })
                                }
                              >
                                确认导入（下次启动应用）
                              </Button>
                            ) : undefined
                          }
                        />
                      ) : null}

                      <Paragraph type="secondary" className="layout-margin-top-16 layout-margin-bottom-0">
                        导入只替换业务数据库，保留当前访问密钥、GitHub 配置和运行设置，清除 Web 会话；不覆盖 MTProto 会话、频道缓存或网盘凭据。
                      </Paragraph>
                    </Space>
                  ),
                },
                {
                  key: "cloud",
                  label: (
                    <Space>
                      <CloudOutlined />
                      <span>云盘配置加密恢复</span>
                    </Space>
                  ),
                  children: (
                    <Space direction="vertical" size="middle" className="field-width-full">
                      <Alert
                        type="warning"
                        showIcon
                        message="云盘备份包含挂载密钥，使用自定义密码进行高强度加密；忘记密码后无法恢复，系统不保存明文。"
                      />

                      <Space wrap>
                        <Button
                          type="primary"
                          icon={<CloudOutlined />}
                          onClick={() => setCloudExportModalOpen(true)}
                        >
                          导出云盘加密备份
                        </Button>
                        {cloudBackupStatus.data?.rollback_available ? (
                          <Button
                            danger
                            loading={rollbackCloud.pending}
                            onClick={() =>
                              confirm({
                                intent: "danger",
                                title: "确认恢复上一个云盘配置",
                                content: CONFIRM_CLOUD_DRIVE_ROLLBACK_TEXT,
                                action: () => rollbackCloud.run(undefined),
                              })
                            }
                          >
                            恢复上一个云盘配置
                          </Button>
                        ) : null}
                      </Space>

                      <div className="cloud-backup-import">
                        <Text strong>导入云盘加密备份</Text>
                        <Paragraph type="secondary" className="layout-margin-top-4 layout-margin-bottom-8">
                          上传与密码验证只会生成候选，不会立即生效；确认后将整体替换所有网盘目的地和凭据。
                        </Paragraph>
                        <Space wrap>
                          <Upload
                            name="cloud_backup"
                            accept=".zip,application/zip"
                            maxCount={1}
                            showUploadList={false}
                            disabled={importCloudAction.pending}
                            beforeUpload={(file) => {
                              setSelectedCloudBackup(file);
                              return false;
                            }}
                          >
                            <Button icon={<UploadOutlined />}>选择云盘 ZIP 备份</Button>
                          </Upload>
                          <Input.Password
                            className="field-width-200"
                            autoComplete="new-password"
                            aria-label="备份密码"
                            placeholder="输入解密密码"
                            value={cloudImportPassword}
                            disabled={importCloudAction.pending}
                            onChange={(e) => setCloudImportPassword(e.target.value)}
                          />
                          <Button
                            type="primary"
                            loading={importCloudAction.pending}
                            disabled={!selectedCloudBackup || cloudImportPassword.length === 0}
                            onClick={() =>
                              selectedCloudBackup &&
                              void importCloudAction.run({
                                backup: selectedCloudBackup,
                                password: cloudImportPassword,
                              })
                            }
                          >
                            上传并验证
                          </Button>
                        </Space>
                        {selectedCloudBackup ? (
                          <div className="layout-margin-top-8">
                            <Text type="secondary">
                              已选择：{selectedCloudBackup.name}（{fmtBytes(selectedCloudBackup.size)}）
                            </Text>
                          </div>
                        ) : null}
                      </div>

                      {pendingCloudBackup ? (
                        <Alert
                          type="info"
                          showIcon
                          message="候选云盘备份已通过验证，尚未应用"
                          description={
                            <Descriptions size="small" column={1} className="cloud-backup-metadata">
                              <Descriptions.Item label="创建时间">
                                {pendingCloudBackup.created_at || "—"}
                              </Descriptions.Item>
                              <Descriptions.Item label="目的地">
                                {pendingCloudBackup.destination_names?.join("、") || "无"}
                              </Descriptions.Item>
                              <Descriptions.Item label="目的地数量">
                                {pendingCloudBackup.destination_names?.length ?? 0}
                              </Descriptions.Item>
                              <Descriptions.Item label="状态">待确认</Descriptions.Item>
                            </Descriptions>
                          }
                          action={
                            <Space wrap>
                              <Button
                                danger
                                type="primary"
                                loading={confirmCloudImport.pending}
                                onClick={() =>
                                  confirm({
                                    intent: "danger",
                                    title: "确认整体替换云盘配置",
                                    content: CONFIRM_CLOUD_DRIVE_IMPORT_TEXT,
                                    action: () => confirmCloudImport.run(undefined),
                                  })
                                }
                              >
                                确认整体替换
                              </Button>
                              <Button
                                loading={cancelCloudPending.pending}
                                onClick={() => void cancelCloudPending.run(undefined)}
                              >
                                取消候选
                              </Button>
                            </Space>
                          }
                        />
                      ) : null}
                    </Space>
                  ),
                },
              ]}
            />
          </PageSection>
        </Space>
      </PageQueryState>

      <FormModal<ExportCloudBackupFormValues>
        title="导出云盘配置加密备份"
        open={cloudExportModalOpen}
        form={cloudExportForm}
        onOpenChange={setCloudExportModalOpen}
        submitText="导出备份"
        onSubmit={async (values) => (await exportCloudAction.run(values)) !== undefined}
      >
        <Alert
          className="layout-margin-bottom-12"
          type="warning"
          showIcon
          message="请妥善保管密码：忘记密码后无法恢复此备份。"
        />
        <Form.Item
          name="password"
          label="备份密码"
          rules={[
            { required: true, message: "请输入备份密码。" },
            { min: 8, message: "备份密码至少 8 个字符。" },
          ]}
        >
          <Input.Password autoComplete="new-password" placeholder="至少 8 个字符" />
        </Form.Item>
        <Form.Item
          name="password_confirmation"
          label="确认密码"
          dependencies={["password"]}
          rules={[
            { required: true, message: "请再次输入备份密码。" },
            ({ getFieldValue }) => ({
              validator: async (_rule, value: string) => {
                if (!value || getFieldValue("password") === value) return;
                throw new Error("两次输入的备份密码不一致。");
              },
            }),
          ]}
        >
          <Input.Password autoComplete="new-password" placeholder="再次输入备份密码" />
        </Form.Item>
      </FormModal>
    </PageScaffold>
  );
}
