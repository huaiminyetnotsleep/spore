/**
 * 云盘下载设置页（/cloud-drive）：/download 指令与「存到网盘」补存的配置入口。
 * 结构：全局总开关（enabled，保存于 data/cloud-drive.json）+ 多目的地卡片列表
 * （名称/类型/路径前缀/启用/默认 Radio + options 通用键值对编辑器）。
 * 类型切换按内置建议键数据表预填空值行（仅当该目的地尚无 options）；
 * pass/2fa/secret/token/key 类敏感键名的值用密码输入掩码显示；
 * 「测试」按钮对已保存的目的地执行 rclone 只读探测（按目的地行级 pending），
 * 结果行内 Alert 展示服务端受控 message。保存为整表单一次 PUT，服务端全量
 * 校验并返回受控 400 文案。备份导出走 Form Modal（pending 防重复、关闭重置）；
 * 回滚/确认导入为整体替换级破坏操作，统一 danger 二次确认。
 */
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { DownloadOutlined, UploadOutlined } from "@ant-design/icons";
import {
  Alert,
  AutoComplete,
  Button,
  Descriptions,
  Form,
  Input,
  Radio,
  Space,
  Switch,
  Tag,
  Typography,
  Upload,
} from "antd";
import type { FormListFieldData, FormListOperation } from "antd/es/form/FormList";

import { fetchCloudDrive, fetchCloudDriveBackupStatus } from "../../api/admin";
import {
  cancelCloudDriveBackupPending,
  confirmCloudDriveBackupImport,
  exportCloudDriveBackup,
  importCloudDriveBackup,
  rollbackCloudDriveBackup,
  saveCloudDrive,
  testCloudDrive,
  type CloudDriveSaveInput,
} from "../../api/mutations";
import { fmtBytes } from "../../shared/format";
import { errorText, useAdminAction, useConfirmAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
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

interface DestinationFormValues {
  name?: string;
  type?: string;
  path_prefix?: string;
  enabled?: boolean;
  options?: CloudOptionRow[];
}

interface CloudDriveFormValues {
  enabled?: boolean;
  default_destination?: string;
  destinations?: DestinationFormValues[];
}

interface ExportBackupFormValues {
  password: string;
  password_confirmation: string;
}

export const CONFIRM_CLOUD_DRIVE_IMPORT_TEXT =
  "确认用候选备份整体替换当前云盘配置？所有目的地和凭据会立即切换；当前配置会保留为最近一次可回滚版本。";

export const CONFIRM_CLOUD_DRIVE_ROLLBACK_TEXT =
  "确认恢复上一个云盘配置？当前配置将被整体替换并立即生效。";

/** 表单值 → PUT 载荷：options 行转对象，两端空白裁剪（键值空行已在行级校验拦截）。 */
function toSaveInput(values: CloudDriveFormValues): CloudDriveSaveInput {
  return {
    enabled: values.enabled ?? false,
    default_destination: values.default_destination ?? "",
    destinations: (values.destinations ?? []).map((dest) => ({
      name: dest.name?.trim() ?? "",
      type: dest.type?.trim() ?? "",
      path_prefix: dest.path_prefix?.trim() ?? "",
      enabled: dest.enabled ?? false,
      options: Object.fromEntries(
        (dest.options ?? [])
          .filter((row) => row.key.trim() !== "" || row.value.trim() !== "")
          .map((row) => [row.key.trim(), row.value.trim()]),
      ),
    })),
  };
}

export function CloudDrivePage() {
  const [form] = Form.useForm<CloudDriveFormValues>();
  const [exportForm] = Form.useForm<ExportBackupFormValues>();
  const [exportModalOpen, setExportModalOpen] = useState(false);
  const [selectedBackup, setSelectedBackup] = useState<File | null>(null);
  const [importPassword, setImportPassword] = useState("");
  const destinationsWatch = Form.useWatch("destinations", form);
  const enabledWatch = Form.useWatch("enabled", form);
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["cloud-drive"],
    queryFn: fetchCloudDrive,
  });
  const backupStatus = useQuery({
    queryKey: ["cloud-drive-backup"],
    queryFn: fetchCloudDriveBackupStatus,
  });
  const confirm = useConfirmAction();

  // 服务端配置异步返回后显式回填（initialValues 只在首挂载读取）。
  // options 对象转为键值行编辑器的数组形态。
  useEffect(() => {
    if (!data) return;
    form.setFieldsValue({
      enabled: data.enabled,
      default_destination: data.default_destination,
      destinations: data.destinations.map((dest) => ({
        name: dest.name,
        type: dest.type,
        path_prefix: dest.path_prefix,
        enabled: dest.enabled,
        options: Object.entries(dest.options ?? {}).map(([key, value]) => ({ key, value })),
      })),
    });
  }, [data, form]);

  // 逐目的地连通性测试：同一时间只允许一个在途（网盘管理接口有限流风险），
  // 结果按 Form.List 稳定 key 行内展示，不弹全局提示。
  const [testingKey, setTestingKey] = useState<number | null>(null);
  const [testResults, setTestResults] = useState<Record<number, { ok: boolean; message: string }>>(
    {},
  );
  const runTest = async (fieldKey: number, name: string) => {
    setTestingKey(fieldKey);
    try {
      const result = await testCloudDrive(name);
      setTestResults((current) => ({ ...current, [fieldKey]: result }));
    } catch (err) {
      setTestResults((current) => ({ ...current, [fieldKey]: { ok: false, message: errorText(err) } }));
    } finally {
      setTestingKey(null);
    }
  };

  const save = useAdminAction({
    action: (values: CloudDriveFormValues) => saveCloudDrive(toSaveInput(values)),
    invalidate: [["cloud-drive"]],
    successText: "云盘下载配置已保存。",
    onDone: () => setTestResults({}),
  });

  const exportAction = useAdminAction<{ exported: true }, ExportBackupFormValues>({
    // exportCloudDriveBackup 为 Promise<void>：映射为显式成功标记，
    // 让 FormModal 能区分「成功关闭」与「失败保留草稿」。
    action: async (values) => {
      await exportCloudDriveBackup(values.password);
      return { exported: true };
    },
    successText: "云盘配置加密备份已导出，请查收浏览器下载。",
    // 弹窗关闭与表单重置由 FormModal 负责
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

  /** 列表内名称唯一校验（排除自身行；服务端保存时仍会全量复核）。 */
  const validateNameUnique = (index: number) => async (_rule: unknown, value: string) => {
    if (!value) return;
    const rows = form.getFieldValue("destinations") as DestinationFormValues[] | undefined;
    if ((rows ?? []).some((row, i) => i !== index && row?.name === value)) {
      throw new Error("名称已存在：目的地名称在列表内必须唯一。");
    }
  };

  /** 类型切换 → 按建议键数据表预填空值行（仅当该目的地尚无 options）。 */
  const prefillSuggestedKeys = (index: number, typeValue: string) => {
    const keys = DEST_TYPE_SUGGESTED_KEYS[typeValue.trim()];
    if (!keys) return;
    const current = form.getFieldValue([
      "destinations",
      index,
      "options",
    ]) as CloudOptionRow[] | undefined;
    if (current && current.length > 0) return;
    form.setFieldValue(
      ["destinations", index, "options"],
      keys.map((key) => ({ key, value: "" })),
    );
  };

  /** 删除目的地：若它是当前默认目的地，同步清空默认选择。 */
  const removeDestination = (
    index: number,
    rowName: string,
    remove: FormListOperation["remove"],
  ) => {
    if (rowName && form.getFieldValue("default_destination") === rowName) {
      form.setFieldValue("default_destination", "");
    }
    remove(index);
  };

  const addDestination = (add: FormListOperation["add"]) => {
    add({ name: "", type: "", path_prefix: "", enabled: true, options: [] });
  };

  const pendingBackup = backupStatus.data?.pending ? backupStatus.data : null;

  return (
    <PageScaffold
      title="云盘下载"
      description="配置 /download 指令与「存到网盘」补存的多网盘目的地；配置为整表保存，备份恢复为整体替换。"
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
          <Form<CloudDriveFormValues>
            form={form}
            layout="vertical"
            className="field-width-full settings-form"
            disabled={isPending}
            onFinish={(values) => void save.run(values)}
          >
            <PageSection
              title="全局开关"
              extra={enabledWatch ? <Tag color="green">已开启</Tag> : <Tag>已关闭</Tag>}
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
                  <Form.Item name="enabled" valuePropName="checked" className="layout-margin-0">
                    <Switch aria-label="云盘下载总开关" />
                  </Form.Item>
                  <Text>开启云盘下载（/download 指令与「存到网盘」补存）</Text>
                </Space>
                <Text type="secondary" className="layout-margin-block-end-0">
                  关闭时授权用户使用 /download 会收到「云盘下载功能未开启」，裸链接请求不受任何影响；开启前需保存至少一个启用的目的地并将其设为默认。
                </Text>
              </Space>
            </PageSection>

            <PageSection title="目的地">
              {/* default_destination 绑定在包裹整列表的 Radio.Group 上：每个目的地
                  卡片内一个 Radio，取值跟随该行当前编辑的名称。 */}
              <Form.Item noStyle name="default_destination" initialValue="">
                <Radio.Group className="cloud-dest-list">
                  <Form.List name="destinations">
                    {(fields: FormListFieldData[], { add, remove }) => (
                      <>
                        {fields.length === 0 ? (
                          <div className="cloud-dest-empty">
                            <Text type="secondary">
                              尚未配置目的地。添加一个网盘目的地（如 MEGA），填写参数并测试连通后，即可在上方开启云盘下载。
                            </Text>
                            <Button type="primary" onClick={() => addDestination(add)}>
                              添加目的地
                            </Button>
                          </div>
                        ) : null}
                        {fields.map((field) => {
                          const row = destinationsWatch?.[field.name];
                          const rowName = row?.name ?? "";
                          const testResult = testResults[field.key];
                          return (
                            <div className="cloud-dest-card" key={field.key}>
                              <div className="cloud-dest-card__head">
                                <Radio value={rowName} disabled={!rowName}>
                                  默认
                                </Radio>
                                <Form.Item
                                  name={[field.name, "name"]}
                                  className="layout-margin-0 cloud-dest-card__name"
                                  rules={[
                                    { required: true, message: "名称不能为空。" },
                                    {
                                      pattern: DEST_NAME_PATTERN,
                                      message:
                                        "名称须为小写字母开头，仅含小写字母、数字或连字符，最长 32 字符（禁用下划线）。",
                                    },
                                    { validator: validateNameUnique(field.name) },
                                  ]}
                                >
                                  <Input placeholder="如 mega-1" allowClear />
                                </Form.Item>
                                <Space className="cloud-dest-card__actions">
                                  <Button
                                    size="small"
                                    disabled={!rowName}
                                    loading={testingKey === field.key}
                                    onClick={() => void runTest(field.key, rowName)}
                                  >
                                    测试
                                  </Button>
                                  <Button
                                    size="small"
                                    danger
                                    onClick={() => removeDestination(field.name, rowName, remove)}
                                  >
                                    删除
                                  </Button>
                                </Space>
                              </div>
                              <div className="settings-field-grid">
                                <Form.Item
                                  name={[field.name, "type"]}
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
                                    onChange={(value) => prefillSuggestedKeys(field.name, value)}
                                  />
                                </Form.Item>
                                <Form.Item
                                  name={[field.name, "path_prefix"]}
                                  label="路径前缀"
                                  extra="上传文件的远端根目录（如 spore），留空表示存到网盘根目录。"
                                >
                                  <Input placeholder="如 spore" allowClear />
                                </Form.Item>
                                <Form.Item
                                  name={[field.name, "enabled"]}
                                  label="启用"
                                  valuePropName="checked"
                                >
                                  <Switch aria-label={`启用目的地 ${rowName || "（未命名）"}`} />
                                </Form.Item>
                              </div>
                              <div className="cloud-options">
                                <Text type="secondary" className="cloud-options__label">
                                  参数（options）：rclone 后端参数键值对，管理端可直接填写 MEGA 原始密码，保存时自动混淆；手动编辑配置文件时填写 rclone obscure 混淆值。敏感键名的值以掩码显示。
                                </Text>
                                <Form.List name={[field.name, "options"]}>
                                  {(
                                    optFields: FormListFieldData[],
                                    optOps: FormListOperation,
                                  ) => (
                                    <div className="cloud-options-list">
                                      {optFields.map((optField) => {
                                        const optKey =
                                          row?.options?.[optField.name]?.key ?? "";
                                        const sensitive = SENSITIVE_OPTION_KEY.test(optKey);
                                        return (
                                          <div className="cloud-option-row" key={optField.key}>
                                            <Form.Item
                                              name={[optField.name, "key"]}
                                              className="layout-margin-0 cloud-option-row__key"
                                              rules={[{ required: true, message: "参数键不能为空。" }]}
                                            >
                                              <Input placeholder="参数名，如 user" allowClear />
                                            </Form.Item>
                                            <Form.Item
                                              name={[optField.name, "value"]}
                                              className="layout-margin-0 cloud-option-row__value"
                                              rules={[
                                                { required: true, message: "参数值不能为空。" },
                                              ]}
                                            >
                                              {sensitive ? (
                                                <Input.Password
                                                  autoComplete="new-password"
                                                  placeholder="参数值（敏感参数，掩码显示）"
                                                  visibilityToggle
                                                />
                                              ) : (
                                                <Input placeholder="参数值" allowClear />
                                              )}
                                            </Form.Item>
                                            <Button
                                              size="small"
                                              danger
                                              onClick={() => optOps.remove(optField.name)}
                                              aria-label="删除参数行"
                                            >
                                              删除
                                            </Button>
                                          </div>
                                        );
                                      })}
                                      <Button
                                        size="small"
                                        type="dashed"
                                        onClick={() => optOps.add({ key: "", value: "" })}
                                      >
                                        添加参数
                                      </Button>
                                    </div>
                                  )}
                                </Form.List>
                              </div>
                              {testResult ? (
                                <Alert
                                  type={testResult.ok ? "success" : "error"}
                                  showIcon
                                  message={testResult.message}
                                  className="cloud-dest-test-result"
                                />
                              ) : null}
                            </div>
                          );
                        })}
                        {fields.length > 0 ? (
                          <Button type="dashed" onClick={() => addDestination(add)}>
                            添加目的地
                          </Button>
                        ) : null}
                      </>
                    )}
                  </Form.List>
                </Radio.Group>
              </Form.Item>
              <Text type="secondary" className="layout-margin-block-start-12 settings-note">
                「测试」对已保存配置中的目的地执行只读探测：新增或改名的目的地请先保存再测试；网盘管理接口有限流风险，请按需点击。默认目的地在
                /download 不指定名称时使用；用户可发送 /download 链接 或 /download
                目的地名称 链接 触发云盘下载。
              </Text>
              <FormActions>
                <Button type="primary" htmlType="submit" loading={save.pending}>
                  保存配置
                </Button>
              </FormActions>
              {/* 服务端 400 受控文案统一经 useAdminAction 的 message.error 提示 */}
            </PageSection>
          </Form>

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
                      <Descriptions.Item label="状态">待确认</Descriptions.Item>
                    </Descriptions>
                  }
                  action={
                    <Space wrap>
                      <Button
                        danger
                        type="primary"
                        loading={confirmImport.pending}
                        onClick={() =>
                          confirm({
                            intent: "danger",
                            title: "确认整体替换云盘配置",
                            content: CONFIRM_CLOUD_DRIVE_IMPORT_TEXT,
                            action: () => confirmImport.run(undefined),
                          })
                        }
                      >
                        确认整体替换
                      </Button>
                      <Button
                        loading={cancelPending.pending}
                        onClick={() => void cancelPending.run(undefined)}
                      >
                        取消候选
                      </Button>
                    </Space>
                  }
                />
              ) : backupStatus.isPending ? (
                <Text type="secondary">正在读取备份状态…</Text>
              ) : (
                <Text type="secondary">当前没有待确认的备份候选。</Text>
              )}
            </Space>
          </PageSection>
        </Space>
      </PageQueryState>

      <FormModal<ExportBackupFormValues>
        title="导出云盘配置加密备份"
        open={exportModalOpen}
        form={exportForm}
        onOpenChange={setExportModalOpen}
        submitText="导出备份"
        onSubmit={async (values) => (await exportAction.run(values)) !== undefined}      >
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
