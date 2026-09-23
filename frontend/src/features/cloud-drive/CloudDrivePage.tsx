/**
 * 云盘下载设置页（/cloud-drive）：/download 指令与「存到网盘」补存的配置入口。
 * 结构：全局总开关（enabled，实时保存生效）+ 目的地表格管理（默认单选/启用开关/测试/删除直接生效，
 * 配置与新增通过弹窗表单交互，弹窗内支持保存前连通性自测）。
 * 展开行提供参数配置只读概览；
 * pass/2fa/secret/token/key 类敏感键名的值用密码输入掩码显示；
 * 备份导出走 Form Modal；回滚/确认导入为整体替换级破坏操作，统一 danger 二次确认。
 */
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  DeleteOutlined,
  DownloadOutlined,
  PlusOutlined,
  RightOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import {
  Alert,
  AutoComplete,
  Button,
  Descriptions,
  Form,
  Input,
  Modal,
  Radio,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  Upload,
} from "antd";

import {
  fetchCloudDrive,
  fetchCloudDriveBackupStatus,
  type CloudDriveView,
} from "../../api/admin";
import {
  cancelCloudDriveBackupPending,
  confirmCloudDriveBackupImport,
  exportCloudDriveBackup,
  importCloudDriveBackup,
  rollbackCloudDriveBackup,
  saveCloudDrive,
  testCloudDrive,
  type CloudDriveTestResult,
} from "../../api/mutations";
import { fmtBytes } from "../../shared/format";
import { errorText, useAdminAction, useConfirmAction } from "../shared/actions";
import { FormModal } from "../shared/FormModal";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState, QueryError } from "../shared/QueryStates";

const { Text } = Typography;

/** 目的地名称规则（与服务端一致：禁 `_`，避免 rclone 环境变量映射撞名）。 */
const DEST_NAME_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;

/** 下载功能与各网盘 options 参数说明。文档随仓库发布，部署后可直接打开。 */
const DOWNLOAD_DOC_URL = "https://github.com/huaiminyetnotsleep/spore/blob/main/docs/guide/download.md";

/** 类型下拉的常用项（AutoComplete 支持自由输入其他 rclone 后端类型）。 */
const DEST_TYPE_OPTIONS = ["mega", "s3", "webdav"].map((value) => ({ value }));

/** 类型 → 建议参数键数据表（纯预填建议，非表单模板；仅当目的地尚无 options 时预填）。 */
const DEST_TYPE_SUGGESTED_KEYS: Record<string, string[]> = {
  mega: ["user", "pass", "2fa"],
  s3: ["provider", "access_key_id", "secret_access_key", "endpoint"],
  webdav: ["url", "vendor", "user", "pass"],
};

/** 敏感参数键名（值经 rclone obscure 或凭据类）：value 输入用密码掩码。 */
const SENSITIVE_OPTION_KEY = /pass|2fa|secret|token|key/i;

/** options 键值对编辑器的单行表单值。 */
interface CloudOptionRow {
  key: string;
  value: string;
}

interface DestinationModalFormValues {
  name: string;
  type: string;
  path_prefix?: string;
  enabled?: boolean;
  options?: CloudOptionRow[];
}

interface ExportBackupFormValues {
  password: string;
  password_confirmation: string;
}

export const CONFIRM_CLOUD_DRIVE_IMPORT_TEXT =
  "确认用候选备份整体替换当前云盘配置？所有目的地和凭据会立即切换；当前配置会保留为最近一次可回滚版本。";

export const CONFIRM_CLOUD_DRIVE_ROLLBACK_TEXT =
  "确认恢复上一个云盘配置？当前配置将被整体替换并立即生效。";

export function CloudDrivePage() {
  const [modalForm] = Form.useForm<DestinationModalFormValues>();
  const [exportForm] = Form.useForm<ExportBackupFormValues>();
  const [exportModalOpen, setExportModalOpen] = useState(false);
  const [selectedBackup, setSelectedBackup] = useState<File | null>(null);
  const [importPassword, setImportPassword] = useState("");

  // 目的地弹窗表单状态
  const [modalOpen, setModalOpen] = useState(false);
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [modalTesting, setModalTesting] = useState(false);
  const [modalTestResult, setModalTestResult] = useState<CloudDriveTestResult | null>(null);

  // 表格展开行受控与连通性测试结果缓存
  const [expandedRowKeys, setExpandedRowKeys] = useState<string[]>([]);
  const [testingName, setTestingName] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<Record<string, CloudDriveTestResult>>({});

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["cloud-drive"],
    queryFn: fetchCloudDrive,
  });
  const backupStatus = useQuery({
    queryKey: ["cloud-drive-backup"],
    queryFn: fetchCloudDriveBackupStatus,
  });
  const confirm = useConfirmAction();

  const save = useAdminAction({
    action: saveCloudDrive,
    invalidate: [["cloud-drive"]],
    successText: "云盘下载配置已保存。",
    onDone: () => setTestResults({}),
  });

  const exportAction = useAdminAction<{ exported: true }, ExportBackupFormValues>({
    action: async (values) => {
      await exportCloudDriveBackup(values.password);
      return { exported: true };
    },
    successText: "云盘配置加密备份已导出，请查收浏览器下载。",
  });

  const importAction = useAdminAction({
    action: ({ backup, password }: { backup: File; password: string }) =>
      importCloudDriveBackup(backup, password),
    invalidate: [["cloud-drive-backup"]],
    successText: "备份已验证，尚未应用。",
    onDone: () => {
      setSelectedBackup(null);
      setImportPassword("");
    },
  });

  const confirmImport = useAdminAction({
    action: () => confirmCloudDriveBackupImport(),
    invalidate: [["cloud-drive"], ["cloud-drive-backup"]],
    successText: "云盘配置已从候选备份恢复并立即生效。",
    onDone: () => setTestResults({}),
  });

  const cancelPending = useAdminAction({
    action: () => cancelCloudDriveBackupPending(),
    invalidate: [["cloud-drive-backup"]],
    successText: "待确认的云盘配置候选已取消。",
  });

  const rollback = useAdminAction({
    action: () => rollbackCloudDriveBackup(),
    invalidate: [["cloud-drive"], ["cloud-drive-backup"]],
    successText: "已恢复上一个云盘配置并立即生效。",
    onDone: () => setTestResults({}),
  });

  // 全局开关切换：实时生效
  const handleToggleGlobal = async (checked: boolean) => {
    if (!data) return;
    await save.run({
      enabled: checked,
      default_destination: data.default_destination,
      destinations: data.destinations,
    });
  };

  // 设置默认目的地：实时生效
  const handleSetDefault = async (destName: string) => {
    if (!data || data.default_destination === destName) return;
    await save.run({
      enabled: data.enabled,
      default_destination: destName,
      destinations: data.destinations,
    });
  };

  // 行内切换启用状态：实时生效
  const handleToggleEnabled = async (index: number, checked: boolean) => {
    if (!data) return;
    const nextDestinations = data.destinations.map((d, i) =>
      i === index ? { ...d, enabled: checked } : d,
    );
    await save.run({
      enabled: data.enabled,
      default_destination: data.default_destination,
      destinations: nextDestinations,
    });
  };

  // 行内删除目的地：二次确认后实时生效
  const handleDeleteDestination = (index: number, name: string) => {
    confirm({
      intent: "danger",
      title: "确认删除目的地",
      content: name
        ? `确认删除目的地「${name}」？删除后立即生效。`
        : "确认删除该目的地？删除后立即生效。",
      okText: "删除",
      action: async () => {
        if (!data) return;
        const nextDestinations = data.destinations.filter((_, i) => i !== index);
        const nextDefault = data.default_destination === name ? "" : data.default_destination;
        await save.run({
          enabled: data.enabled,
          default_destination: nextDefault,
          destinations: nextDestinations,
        });
      },
    });
  };

  // 表格单行连通性测试
  const runRowTest = async (name: string) => {
    setTestingName(name);
    setExpandedRowKeys((prev) => (prev.includes(name) ? prev : [...prev, name]));
    try {
      const result = await testCloudDrive(name);
      setTestResults((prev) => ({ ...prev, [name]: result }));
    } catch (err) {
      setTestResults((prev) => ({ ...prev, [name]: { ok: false, message: errorText(err) } }));
    } finally {
      setTestingName(null);
    }
  };

  // 打开新增目的地弹窗
  const openAddModal = () => {
    setEditingIndex(null);
    setModalTestResult(null);
    modalForm.setFieldsValue({
      name: "",
      type: "",
      path_prefix: "",
      enabled: true,
      options: [],
    });
    setModalOpen(true);
  };

  // 打开编辑目的地弹窗
  const openEditModal = (index: number) => {
    if (!data) return;
    const dest = data.destinations[index];
    setEditingIndex(index);
    setModalTestResult(null);
    modalForm.setFieldsValue({
      name: dest.name,
      type: dest.type,
      path_prefix: dest.path_prefix,
      enabled: dest.enabled,
      options: Object.entries(dest.options ?? {}).map(([key, value]) => ({ key, value })),
    });
    setModalOpen(true);
  };

  // 弹窗类型切换：按建议键预填
  const handleModalTypeChange = (typeValue: string) => {
    const keys = DEST_TYPE_SUGGESTED_KEYS[typeValue.trim()];
    if (!keys) return;
    const current = modalForm.getFieldValue("options") as CloudOptionRow[] | undefined;
    if (current && current.length > 0) return;
    modalForm.setFieldValue(
      "options",
      keys.map((key) => ({ key, value: "" })),
    );
  };

  // 弹窗名称唯一校验
  const validateModalNameUnique = async (_rule: unknown, value: string) => {
    if (!value || !data) return;
    const val = value.trim();
    const exists = data.destinations.some((d, i) => i !== editingIndex && d.name === val);
    if (exists) {
      throw new Error("名称已存在：目的地名称在列表内必须唯一。");
    }
  };

  // 弹窗内保存前测试连通性
  const handleTestInModal = async () => {
    try {
      await modalForm.validateFields();
      const values = modalForm.getFieldsValue();
      const name = values.name?.trim() ?? "";
      const type = values.type?.trim() ?? "";
      const path_prefix = values.path_prefix?.trim() ?? "";
      const enabled = values.enabled ?? true;
      const optionsObj = Object.fromEntries(
        (values.options ?? [])
          .filter((row) => row.key?.trim() !== "" || row.value?.trim() !== "")
          .map((row) => [row.key.trim(), row.value?.trim() ?? ""]),
      );
      setModalTesting(true);
      const result = await testCloudDrive({
        destination: {
          name,
          type,
          path_prefix,
          enabled,
          options: optionsObj,
        },
      });
      setModalTestResult(result);
    } catch (err) {
      if (err && typeof err === "object" && "errorFields" in err) {
        return;
      }
      setModalTestResult({ ok: false, message: errorText(err) });
    } finally {
      setModalTesting(false);
    }
  };

  // 弹窗表单提交：直接保存生效
  const handleSaveDestination = async (values: DestinationModalFormValues) => {
    if (!data) return;
    const name = values.name.trim();
    const type = values.type.trim();
    const path_prefix = values.path_prefix?.trim() ?? "";
    const enabled = values.enabled ?? true;
    const options = Object.fromEntries(
      (values.options ?? [])
        .filter((row) => row.key?.trim() !== "" || row.value?.trim() !== "")
        .map((row) => [row.key.trim(), row.value?.trim() ?? ""]),
    );
    const newDest = {
      name,
      type,
      path_prefix,
      enabled,
      options,
    };

    let nextDestinations: CloudDriveView["destinations"];
    let nextDefault = data.default_destination;

    if (editingIndex === null) {
      nextDestinations = [...data.destinations, newDest];
      if (!nextDefault) {
        nextDefault = name;
      }
    } else {
      const oldName = data.destinations[editingIndex]?.name ?? "";
      nextDestinations = data.destinations.map((d, i) =>
        i === editingIndex ? newDest : d,
      );
      if (nextDefault === oldName) {
        nextDefault = name;
      }
    }

    const res = await save.run({
      enabled: data.enabled,
      default_destination: nextDefault,
      destinations: nextDestinations,
    });
    if (res !== undefined) {
      setModalOpen(false);
    }
  };

  const pendingBackup = backupStatus.data?.pending ? backupStatus.data : null;

  const destinations = data?.destinations ?? [];

  const columns = [
    {
      title: "默认",
      key: "default",
      width: 70,
      align: "center" as const,
      render: (_: unknown, record: CloudDriveView["destinations"][0]) => (
        <Radio
          checked={data?.default_destination === record.name}
          disabled={!record.name || save.pending}
          onChange={() => void handleSetDefault(record.name)}
          aria-label={`默认目的地 ${record.name}`}
        />
      ),
    },
    {
      title: "目的地",
      key: "name",
      render: (_: unknown, record: CloudDriveView["destinations"][0]) => (
        <Space size="small">
          <Text strong className="cloud-dest-card__title">
            {record.name}
          </Text>
          {record.type ? (
            <Tag className="cloud-dest-card__type-tag" color="blue">
              {record.type}
            </Tag>
          ) : null}
        </Space>
      ),
    },
    {
      title: "路径前缀",
      key: "path_prefix",
      render: (_: unknown, record: CloudDriveView["destinations"][0]) =>
        record.path_prefix ? <code>{record.path_prefix}</code> : <Text type="secondary">（根目录）</Text>,
    },
    {
      title: "启用",
      key: "enabled",
      width: 90,
      render: (_: unknown, record: CloudDriveView["destinations"][0], index: number) => (
        <Switch
          checked={record.enabled}
          disabled={save.pending}
          onChange={(checked) => void handleToggleEnabled(index, checked)}
          checkedChildren="启用"
          unCheckedChildren="禁用"
          aria-label={`启用目的地 ${record.name}`}
        />
      ),
    },
    {
      title: "操作",
      key: "actions",
      width: 180,
      render: (_: unknown, record: CloudDriveView["destinations"][0], index: number) => (
        <Space size="small">
          <Button
            size="small"
            type="link"
            onClick={() => openEditModal(index)}
          >
            配置
          </Button>
          <Button
            size="small"
            disabled={!record.name}
            loading={testingName === record.name}
            onClick={() => void runRowTest(record.name)}
          >
            测试
          </Button>
          <Button
            size="small"
            danger
            disabled={save.pending}
            onClick={() => handleDeleteDestination(index, record.name)}
          >
            删除
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <PageScaffold
      title="云盘下载"
      description="配置 /download 指令与「存到网盘」补存的多网盘目的地；配置项操作直接生效，备份恢复为整体替换。"
      actions={
        <Button href={DOWNLOAD_DOC_URL} target="_blank" rel="noopener noreferrer">
          查看下载配置文档
        </Button>
      }
    >
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="middle" className="field-width-full">
          <PageSection
            title="全局开关"
            extra={data?.enabled ? <Tag color="green">已开启</Tag> : <Tag>已关闭</Tag>}
          >
            <Space direction="vertical" size="small" className="field-width-full">
              {data && !data.rclone_available ? (
                <Alert
                  type="warning"
                  showIcon
                  message="未检测到 rclone 二进制，云盘上传与目的地测试暂不可用"
                  description="官方镜像已内置固定版本 rclone；自定义部署可通过 RCLONE_BIN 环境变量指定路径，安装后重启服务生效。"
                />
              ) : null}
              <Space wrap>
                <Switch
                  checked={data?.enabled ?? false}
                  disabled={save.pending || !data}
                  onChange={(checked) => void handleToggleGlobal(checked)}
                  aria-label="云盘下载总开关"
                />
                <Text>开启云盘下载（/download 指令与「存到网盘」补存）</Text>
              </Space>
              <Text type="secondary" className="layout-margin-block-end-0">
                关闭时授权用户使用 /download 会收到「云盘下载功能未开启」，裸链接请求不受任何影响；开启前需保存至少一个启用的目的地并将其设为默认。
              </Text>
            </Space>
          </PageSection>

          <PageSection title="目的地">
            {destinations.length === 0 ? (
              <div className="cloud-dest-empty">
                <Text type="secondary">
                  尚未配置目的地。添加一个网盘目的地（如 MEGA），填写参数并测试连通后，即可在上方开启云盘下载。
                </Text>
                <Button
                  type="primary"
                  onClick={openAddModal}
                >
                  添加目的地
                </Button>
              </div>
            ) : (
              <div className="cloud-dest-table-wrap">
                <div className="cloud-dest-table-toolbar">
                  <Text type="secondary">已配置 {destinations.length} 个网盘目的地</Text>
                  <Button
                    type="dashed"
                    size="small"
                    icon={<PlusOutlined />}
                    onClick={openAddModal}
                  >
                    添加目的地
                  </Button>
                </div>
                <Table<CloudDriveView["destinations"][0]>
                  className="cloud-dest-table"
                  rowKey="name"
                  dataSource={destinations}
                  columns={columns}
                  pagination={false}
                  scroll={{ x: "max-content" }}
                  rowClassName={(record) =>
                    record.name && record.name === data?.default_destination
                      ? "cloud-dest-row--highlight"
                      : ""
                  }
                  expandable={{
                    expandIcon: ({ expanded, onExpand, record }) => (
                      <Button
                        type="text"
                        size="small"
                        className="cloud-dest-expand-btn"
                        icon={<RightOutlined rotate={expanded ? 90 : 0} />}
                        onClick={(e) => onExpand(record, e)}
                        aria-label={expanded ? `收起目的地 ${record.name} 详情` : `展开目的地 ${record.name} 详情`}
                      />
                    ),
                    expandedRowKeys,
                    onExpandedRowsChange: (keys) => setExpandedRowKeys(keys as string[]),
                    expandedRowRender: (record, _index, _indent, expanded) => {
                      if (!expanded) return null;
                      const testResult = testResults[record.name];
                      const optionsEntries = Object.entries(record.options ?? {});
                      return (
                        <div className="cloud-dest-expanded">
                          <div className="cloud-dest-detail">
                            {testResult ? (
                              <Alert
                                type={testResult.ok ? "success" : "error"}
                                showIcon
                                message={testResult.message}
                                className="cloud-dest-test-result"
                              />
                            ) : null}
                            <div className="cloud-dest-detail__section">
                              <div className="cloud-dest-detail__grid">
                                <div className="cloud-dest-detail__item">
                                  <span className="cloud-dest-detail__label">目的地名称</span>
                                  <span className="cloud-dest-detail__value">{record.name}</span>
                                </div>
                                <div className="cloud-dest-detail__item">
                                  <span className="cloud-dest-detail__label">后端类型</span>
                                  <span className="cloud-dest-detail__value">
                                    <Tag color="blue">{record.type}</Tag>
                                  </span>
                                </div>
                                <div className="cloud-dest-detail__item">
                                  <span className="cloud-dest-detail__label">路径前缀</span>
                                  <span className="cloud-dest-detail__value">
                                    {record.path_prefix ? <code>{record.path_prefix}</code> : <Text type="secondary">（根目录）</Text>}
                                  </span>
                                </div>
                                <div className="cloud-dest-detail__item">
                                  <span className="cloud-dest-detail__label">启用状态</span>
                                  <span className="cloud-dest-detail__value">
                                    {record.enabled ? <Tag color="success">已启用</Tag> : <Tag>未启用</Tag>}
                                  </span>
                                </div>
                              </div>
                            </div>
                            <div className="cloud-dest-detail__section">
                              <div className="cloud-options-preview">
                                <Text strong className="cloud-options-preview__title">
                                  参数配置（options）{optionsEntries.length > 0 ? `（${optionsEntries.length} 项）` : ""}
                                </Text>
                                {optionsEntries.length > 0 ? (
                                  <div className="cloud-options-preview__tags">
                                    {optionsEntries.map(([k, v]) => (
                                      <span key={k} className="cloud-option-badge">
                                        <span className="cloud-option-badge__key">{k}</span>
                                        <span className="cloud-option-badge__val">
                                          {SENSITIVE_OPTION_KEY.test(k) ? "••••••••" : v}
                                        </span>
                                      </span>
                                    ))}
                                  </div>
                                ) : (
                                  <Text type="secondary">未配置自定义参数。</Text>
                                )}
                              </div>
                            </div>
                          </div>
                        </div>
                      );
                    },
                  }}
                />
              </div>
            )}
            <Text type="secondary" className="layout-margin-block-start-12 settings-note">
              「测试」对目的地执行只读探测；网盘管理接口有限流风险，请按需点击。默认目的地在 /download
              不指定名称时使用；用户可发送 /download 链接 或 /download 目的地名称 链接 触发云盘下载。
            </Text>
          </PageSection>

          <PageSection title="配置备份与恢复">
            <Space direction="vertical" size="middle" className="field-width-full">
              <Alert
                type="warning"
                showIcon
                message="备份使用你设置的密码加密；忘记密码后无法恢复，系统也不会保存或找回密码。"
              />

              <Space wrap>
                <Button
                  type="primary"
                  icon={<DownloadOutlined />}
                  onClick={() => setExportModalOpen(true)}
                >
                  导出加密备份
                </Button>
                {backupStatus.data?.rollback_available ? (
                  <Button
                    danger
                    loading={rollback.pending}
                    onClick={() =>
                      confirm({
                        intent: "danger",
                        title: "确认恢复上一个云盘配置",
                        content: CONFIRM_CLOUD_DRIVE_ROLLBACK_TEXT,
                        action: () => rollback.run(undefined),
                      })
                    }
                  >
                    恢复上一个配置
                  </Button>
                ) : null}
              </Space>

              <div className="cloud-backup-import">
                <Text strong>导入加密备份</Text>
                <Text type="secondary">
                  上传与密码验证只会生成候选，不会改变当前配置；确认时将整体替换所有目的地和凭据。
                </Text>
                <Space wrap>
                  <Upload
                    name="backup"
                    accept=".zip,application/zip"
                    maxCount={1}
                    showUploadList={false}
                    disabled={importAction.pending}
                    beforeUpload={(file) => {
                      setSelectedBackup(file);
                      return false;
                    }}
                  >
                    <Button icon={<UploadOutlined />}>选择 ZIP 备份</Button>
                  </Upload>
                  <Input.Password
                    className="cloud-backup-password"
                    autoComplete="new-password"
                    aria-label="备份密码"
                    placeholder="输入备份密码"
                    value={importPassword}
                    disabled={importAction.pending}
                    onChange={(event) => setImportPassword(event.target.value)}
                  />
                  <Button
                    type="primary"
                    loading={importAction.pending}
                    disabled={!selectedBackup || importPassword.length === 0}
                    onClick={() =>
                      selectedBackup &&
                      void importAction.run({ backup: selectedBackup, password: importPassword })
                    }
                  >
                    上传并验证
                  </Button>
                </Space>
                {selectedBackup ? (
                  <Text type="secondary">
                    已选择：{selectedBackup.name}（{fmtBytes(selectedBackup.size)}）
                  </Text>
                ) : null}
              </div>

              {backupStatus.isError ? (
                <QueryError onRetry={() => void backupStatus.refetch()} />
              ) : pendingBackup ? (
                <Alert
                  type="info"
                  showIcon
                  message="候选备份已通过验证，尚未应用"
                  description={
                    <Descriptions size="small" column={1} className="cloud-backup-metadata">
                      <Descriptions.Item label="格式版本">
                        {pendingBackup.format_version ?? "—"}
                      </Descriptions.Item>
                      <Descriptions.Item label="创建时间">
                        {pendingBackup.created_at || "—"}
                      </Descriptions.Item>
                      <Descriptions.Item label="哈希摘要">
                        {pendingBackup.sha256
                          ? `${pendingBackup.sha256.slice(0, 12)}${pendingBackup.sha256.length > 12 ? "…" : ""}`
                          : "—"}
                      </Descriptions.Item>
                      <Descriptions.Item label="目的地">
                        {pendingBackup.destination_names?.join("、") || "无"}
                      </Descriptions.Item>
                      <Descriptions.Item label="目的地数量">
                        {pendingBackup.destination_names?.length ?? 0}
                      </Descriptions.Item>
                    </Descriptions>
                  }
                  action={
                    <Space direction="vertical">
                      <Button
                        type="primary"
                        danger
                        loading={confirmImport.pending}
                        onClick={() =>
                          confirm({
                            intent: "danger",
                            title: "确认应用候选备份",
                            content: CONFIRM_CLOUD_DRIVE_IMPORT_TEXT,
                            action: () => confirmImport.run(undefined),
                          })
                        }
                      >
                        确认整体替换
                      </Button>
                      <Button
                        loading={cancelPending.pending}
                        onClick={() => cancelPending.run(undefined)}
                      >
                        取消候选
                      </Button>
                    </Space>
                  }
                />
              ) : null}
            </Space>
          </PageSection>
        </Space>
      </PageQueryState>

      {/* 目的地新增与配置编辑弹窗 */}
      <Modal
        title={
          editingIndex !== null
            ? `配置目的地「${data?.destinations[editingIndex]?.name ?? ""}」`
            : "添加目的地"
        }
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        footer={
          <div className="modal-footer-split">
            <Button
              loading={modalTesting}
              onClick={handleTestInModal}
            >
              测试连通性
            </Button>
            <Space>
              <Button onClick={() => setModalOpen(false)}>取消</Button>
              <Button
                type="primary"
                loading={save.pending}
                onClick={() => modalForm.submit()}
              >
                保存生效
              </Button>
            </Space>
          </div>
        }
        width={640}
        destroyOnHidden
      >
        {modalTestResult ? (
          <Alert
            type={modalTestResult.ok ? "success" : "error"}
            showIcon
            message={modalTestResult.message}
            className="layout-margin-block-end-16"
            closable
            onClose={() => setModalTestResult(null)}
          />
        ) : null}
        <Form<DestinationModalFormValues>
          form={modalForm}
          layout="vertical"
          onFinish={(values) => void handleSaveDestination(values)}
        >
          <div className="settings-field-grid">
            <Form.Item
              name="name"
              label="目的地名称"
              rules={[
                { required: true, message: "名称不能为空。" },
                {
                  pattern: DEST_NAME_PATTERN,
                  message:
                    "名称须为小写字母开头，仅含小写字母、数字或连字符，最长 32 字符（禁用下划线）。",
                },
                { validator: validateModalNameUnique },
              ]}
              extra="仅含小写字母、数字与短横线，最长 32 字符；用于 /download 指定名称。"
            >
              <Input placeholder="如 mega-1" allowClear />
            </Form.Item>
            <Form.Item
              name="type"
              label="类型（rclone 后端）"
              rules={[{ required: true, message: "类型不能为空。" }]}
              extra="常用类型可直接选择，其余 rclone 后端类型可自由输入；切换类型会按建议参数表预填参数键。"
            >
              <AutoComplete
                options={DEST_TYPE_OPTIONS}
                filterOption={(input, option) =>
                  (option?.value ?? "").includes(input.trim().toLowerCase())
                }
                placeholder="如 mega"
                onChange={handleModalTypeChange}
              />
            </Form.Item>
            <Form.Item
              name="path_prefix"
              label="路径前缀"
              extra="上传文件的远端根目录（如 spore），留空表示存到网盘根目录。"
            >
              <Input placeholder="如 spore" allowClear />
            </Form.Item>
            <Form.Item
              name="enabled"
              label="启用状态"
              valuePropName="checked"
              extra="是否在保存后立即启用此目的地。"
            >
              <Switch checkedChildren="启用" unCheckedChildren="禁用" />
            </Form.Item>
          </div>
          <div className="cloud-options-panel">
            <Form.List name="options">
              {(optFields, optOps) => (
                <>
                  <div className="cloud-options-panel__head">
                    <div className="cloud-options-panel__title-wrap">
                      <Text strong className="cloud-options-panel__title">
                        参数配置（options）
                      </Text>
                      <Text type="secondary" className="cloud-options-panel__desc">
                        rclone 后端参数键值对，敏感键名的值以掩码显示；保存时自动混淆敏感凭据。
                      </Text>
                    </div>
                    <Button
                      size="small"
                      type="dashed"
                      icon={<PlusOutlined />}
                      onClick={() => optOps.add({ key: "", value: "" })}
                    >
                      添加参数
                    </Button>
                  </div>
                  <div className="cloud-options-list">
                    {optFields.length > 0 ? (
                      <div className="cloud-options-header">
                        <span className="cloud-options-header__key">参数键 (Key)</span>
                        <span className="cloud-options-header__value">参数值 (Value)</span>
                        <span className="cloud-options-header__action">操作</span>
                      </div>
                    ) : null}
                    {optFields.map((optField) => {
                      const currentOptions = modalForm.getFieldValue("options") as
                        | CloudOptionRow[]
                        | undefined;
                      const optKey = currentOptions?.[optField.name]?.key ?? "";
                      const sensitive = SENSITIVE_OPTION_KEY.test(optKey);
                      return (
                        <div className="cloud-option-row" key={optField.key}>
                          <Form.Item
                            name={[optField.name, "key"]}
                            className="layout-margin-0 cloud-option-row__key"
                            rules={[{ required: true, message: "参数键不能为空。" }]}
                          >
                            <Input placeholder="如 user" allowClear />
                          </Form.Item>
                          <Form.Item
                            name={[optField.name, "value"]}
                            className="layout-margin-0 cloud-option-row__value"
                            rules={[{ required: true, message: "参数值不能为空。" }]}
                          >
                            {sensitive ? (
                              <Input.Password
                                autoComplete="new-password"
                                placeholder="敏感参数值（掩码显示）"
                                visibilityToggle
                              />
                            ) : (
                              <Input placeholder="参数值" allowClear />
                            )}
                          </Form.Item>
                          <div className="cloud-option-row__action">
                            <Button
                              type="text"
                              size="small"
                              danger
                              icon={<DeleteOutlined />}
                              onClick={() => optOps.remove(optField.name)}
                              aria-label="删除参数行"
                              title="删除参数行"
                            >
                              删除
                            </Button>
                          </div>
                        </div>
                      );
                    })}
                    {optFields.length === 0 ? (
                      <div className="cloud-options-empty">
                        <Text type="secondary">
                          暂无自定义参数。切换类型可自动预填建议参数，或点击上方「添加参数」。
                        </Text>
                      </div>
                    ) : null}
                  </div>
                </>
              )}
            </Form.List>
          </div>
        </Form>
      </Modal>

      {/* 备份导出弹窗 */}
      <FormModal<ExportBackupFormValues>
        open={exportModalOpen}
        onOpenChange={setExportModalOpen}
        form={exportForm}
        title="导出云盘配置加密备份"
        submitText="导出备份"
        onSubmit={async (values) => {
          await exportAction.run(values);
          return true;
        }}
      >
        <Space direction="vertical" size="small" className="field-width-full">
          <Text type="secondary">
            备份将使用该密码加密生成 ZIP 文件；恢复时必须输入完全相同的密码。
          </Text>
          <Form.Item
            name="password"
            label="备份密码"
            rules={[
              { required: true, message: "备份密码不能为空。" },
              { min: 8, message: "备份密码至少 8 个字符。" },
            ]}
          >
            <Input.Password
              autoComplete="new-password"
              placeholder="输入至少 8 位强密码"
              aria-label="备份密码"
            />
          </Form.Item>
          <Form.Item
            name="password_confirmation"
            label="确认密码"
            dependencies={["password"]}
            rules={[
              { required: true, message: "请再次输入密码。" },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (!value || getFieldValue("password") === value) {
                    return Promise.resolve();
                  }
                  return Promise.reject(new Error("两次输入的备份密码不一致。"));
                },
              }),
            ]}
          >
            <Input.Password
              autoComplete="new-password"
              placeholder="再次输入备份密码"
              aria-label="确认密码"
            />
          </Form.Item>
        </Space>
      </FormModal>
    </PageScaffold>
  );
}
