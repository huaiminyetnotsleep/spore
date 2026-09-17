/**
 * 备份管理页（SSR /backup 的 SPA 对应实现）。
 * 导出为流式 .db 文件下载（fetch blob，响应头与 SSR 导出一致）；上传经
 * multipart 先校验（不改变当前数据库），确认后才写 marker、下次启动应用。
 * 导出是读取型操作，确认为 default 意图；确认导入是整库替换的最后一步，
 * 保持 danger 二次确认（文案与 SSR data-confirm 对齐）。选择文件后提供
 * 「清除选择」取消入口；失败只展示服务端受控文案，不误报成功。
 * 备份内容不落 localStorage。
 */
import { useQuery } from "@tanstack/react-query";
import { DownloadOutlined, UploadOutlined } from "@ant-design/icons";
import { Alert, Button, Space, Typography, Upload } from "antd";
import { useState } from "react";

import { fetchBackupStatus } from "../../api/admin";
import { confirmBackupImport, exportBackup, uploadBackup } from "../../api/mutations";
import { fmtBytes, fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";

const { Paragraph, Text } = Typography;

/** SSR data-confirm 同款文案：确认导入是整库替换的最后一步。 */
export const CONFIRM_IMPORT_TEXT =
  "确认整库替换业务数据库？当前用户、请求、用量和审计将被候选备份替换，安全设置会保留，操作需要下次重启生效。";

/** SSR data-confirm 同款文案：导出可能耗时数秒。 */
const CONFIRM_EXPORT_TEXT = "确定导出数据库备份？大库可能需要数秒。";

export function BackupPage() {
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["backup"],
    queryFn: fetchBackupStatus,
  });

  const exportAction = useAdminAction({
    action: () => exportBackup(),
    invalidate: [["backup"]],
    successText: "数据库备份已导出，请查收浏览器下载。",
  });

  const upload = useAdminAction({
    action: (file: File) => uploadBackup(file),
    invalidate: [["backup"]],
    successText: (result) => result.message,
    onDone: () => setSelectedFile(null),
  });

  const confirmImport = useAdminAction({
    action: () => confirmBackupImport(),
    invalidate: [["backup"]],
    successText: (result) => result.message,
  });

  // 导出为 default 意图确认（读取型操作）；导入确认为 danger（整库替换）
  const confirm = useConfirmAction();

  return (
    <PageScaffold
      title="数据备份"
      description="导出与导入业务数据库备份；导入为整库替换，需二次确认并在下次重启后生效。"
      actions={
        <Button
          type="primary"
          icon={<DownloadOutlined />}
          loading={exportAction.pending}
          onClick={() =>
            confirm({
              intent: "default",
              title: "确认导出数据库备份",
              content: CONFIRM_EXPORT_TEXT,
              action: () => exportAction.run(undefined),
            })
          }
        >
          导出数据库备份
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
          <DataTable
            density="compact"
            rowKey="item"
            pagination={false}
            loading={isPending}
            columns={[
              { title: "项目", dataIndex: "item", key: "item" },
              { title: "值", dataIndex: "value", key: "value" },
            ]}
            dataSource={[
              {
                item: "数据库文件",
                value: data ? `${data.db_path}（${fmtBytes(data.db_size_bytes)}）` : "—",
              },
              { item: "最近备份", value: data ? fmtTime(data.last_backup_at) : "—" },
            ]}
          />

          <PageSection title="导入业务数据库">
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
                  disabled={upload.pending}
                  beforeUpload={(file) => {
                    // 阻止 antd 自动上传：文件选择后由"上传并校验"显式提交
                    setSelectedFile(file);
                    return false;
                  }}
                >
                  <Button icon={<UploadOutlined />}>选择 .db 文件</Button>
                </Upload>
                <Button
                  type="primary"
                  disabled={!selectedFile}
                  loading={upload.pending}
                  onClick={() => selectedFile && void upload.run(selectedFile)}
                >
                  上传并校验
                </Button>
              </Space>
              {selectedFile ? (
                <Space wrap size={8}>
                  <Text type="secondary">
                    已选择：{selectedFile.name}（{fmtBytes(selectedFile.size)}）
                  </Text>
                  <Button
                    size="small"
                    disabled={upload.pending}
                    onClick={() => setSelectedFile(null)}
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
                        loading={confirmImport.pending}
                        onClick={() =>
                          confirm({
                            intent: "danger",
                            title: "确认导入备份（整库替换）",
                            content: CONFIRM_IMPORT_TEXT,
                            action: () => confirmImport.run(undefined),
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
                导入只替换业务数据库，保留当前访问密钥、GitHub 配置和运行设置，清除 Web
                会话；不覆盖 MTProto session.json、peers.json 或临时媒体。
              </Paragraph>
            </Space>
          </PageSection>
        </Space>
      </PageQueryState>
    </PageScaffold>
  );
}
