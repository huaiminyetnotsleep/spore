import { DeleteOutlined, ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { Button, Input, Space, Typography } from "antd";
import type { TableColumnsType } from "antd";
import { useState } from "react";

import { fetchTempDir, type TempFile } from "../../api/admin";
import { clearTempDir, deleteTempFiles } from "../../api/mutations";
import { fmtBytes, fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { PageSection } from "../shared/PageLayout";
import { RowActions } from "../shared/RowActions";
import type { RowActionItem } from "../shared/RowActions";
import { SectionQueryState } from "../shared/QueryStates";

const { Text } = Typography;

export function TempDirSection({ embedded = false }: { embedded?: boolean }) {
  const [selected, setSelected] = useState<string[]>([]);
  const [keyword, setKeyword] = useState("");
  const query = useQuery({ queryKey: ["temp-dir"], queryFn: fetchTempDir });
  const confirm = useConfirmAction();
  const clearAction = useAdminAction({
    action: clearTempDir,
    invalidate: [["temp-dir"], ["overview"]],
    successText: "临时目录已清理。",
    onDone: () => setSelected([]),
  });
  const deleteAction = useAdminAction({
    action: deleteTempFiles,
    invalidate: [["temp-dir"], ["overview"]],
    successText: (result) => `已删除所选文件，剩余 ${result.temp_dir.file_count} 个文件。`,
    onDone: () => setSelected([]),
  });

  const columns: TableColumnsType<TempFile> = [
    {
      title: "文件路径",
      dataIndex: "path",
      key: "path",
      render: (path: string) => <Text code>{path}</Text>,
    },
    {
      title: "大小",
      dataIndex: "size_bytes",
      key: "size_bytes",
      width: 110,
      render: (size: number) => fmtBytes(size),
    },
    {
      title: "修改时间",
      dataIndex: "modified_at",
      key: "modified_at",
      width: 170,
      render: (time: number) => fmtTime(time),
    },
    {
      title: "操作",
      key: "action",
      // 与其他表格的操作列一致：fixed 右侧 + RowActions 统一链接按钮，
      // 窄屏横向滚动时删除入口始终可见
      width: 80,
      fixed: "right",
      render: (_value, file) => (
        <RowActions
          actions={[
            {
              key: "delete",
              label: <DeleteOutlined />,
              title: "删除",
              danger: true,
              disabled: clearAction.pending || deleteAction.pending,
              onClick: () =>
                confirm({
                  intent: "danger",
                  title: "删除临时文件",
                  content: `确定删除「${file.path}」吗？此操作不可撤销。`,
                  okText: "删除",
                  action: () => deleteAction.run([file.path]),
                }),
            } satisfies RowActionItem,
          ]}
        />
      ),
    },
  ];

  const fileCount = query.data?.file_count ?? 0;
  const description = query.data
    ? `共 ${fileCount} 个文件，占用 ${fmtBytes(query.data.total_bytes)}。`
    : "查看临时文件并安全清理。";
  const files = query.data?.files ?? [];
  const needle = keyword.trim().toLowerCase();
  const visibleFiles = needle
    ? files.filter((file) => file.path.toLowerCase().includes(needle))
    : files;

  const content = (
    <SectionQueryState
      initialLoading={query.isPending}
      error={query.error}
      hasData={query.data !== undefined}
      onRetry={() => void query.refetch()}
    >
      {query.data ? (
        /* 列表页统一布局：FilterBar / 批量操作条 / 表格经垂直 Space 分隔 */
        <Space direction="vertical" size="middle" className="field-width-full">
          {embedded ? <Text type="secondary">{description}</Text> : null}
          <FilterBar
            mode="instant"
            actions={
              <>
                <Button icon={<ReloadOutlined />} loading={query.isFetching} onClick={() => void query.refetch()}>
                  刷新
                </Button>
                <Button
                  danger
                  icon={<DeleteOutlined />}
                  loading={clearAction.pending}
                  disabled={deleteAction.pending}
                  onClick={() => confirm({
                    intent: "danger",
                    title: "清理临时目录",
                    content: `确定清空临时目录中的所有内容吗？当前统计到 ${fileCount} 个普通文件（${fmtBytes(query.data?.total_bytes ?? 0)}）。此操作不可撤销。`,
                    okText: "清理",
                    action: () => clearAction.run(undefined),
                  })}
                >
                  清理全部
                </Button>
              </>
            }
          >
            <Input.Search
              placeholder="文件名"
              allowClear
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              onSearch={setKeyword}
              className="field-width-200"
            />
          </FilterBar>
          {selected.length > 0 ? (
            <Space wrap className="batch-action-bar">
              <Text>已选 {selected.length} 个文件</Text>
              <Button
                danger
                icon={<DeleteOutlined />}
                loading={deleteAction.pending}
                disabled={clearAction.pending}
                onClick={() => confirm({
                  intent: "danger",
                  title: "批量删除临时文件",
                  content: `确定删除选中的 ${selected.length} 个文件吗？此操作不可撤销。`,
                  okText: "删除",
                  action: () => deleteAction.run(selected),
                })}
              >
                批量删除（{selected.length}）
              </Button>
              <Button onClick={() => setSelected([])}>取消选择</Button>
            </Space>
          ) : null}
          {files.length === 0 ? (
            <div className="empty-state" role="status">临时目录中没有可列出的普通文件。</div>
          ) : (
            <DataTable<TempFile>
              density="compact"
              rowKey="path"
              columns={columns}
              dataSource={visibleFiles}
              rowSelection={{
                preserveSelectedRowKeys: true,
                selectedRowKeys: selected,
                onChange: (keys) => setSelected(keys.map(String)),
              }}
              pagination={{ pageSize: 10, showSizeChanger: false, hideOnSinglePage: true }}
              emptyText="没有符合条件的文件。"
            />
          )}
        </Space>
      ) : null}
    </SectionQueryState>
  );

  if (embedded) {
    return content;
  }

  return (
    <PageSection title="临时目录" description={description}>
      {content}
    </PageSection>
  );
}
