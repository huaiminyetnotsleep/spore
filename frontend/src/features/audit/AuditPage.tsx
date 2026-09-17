import { useQuery } from "@tanstack/react-query";
import { Button, DatePicker, Form, Space, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs, { type Dayjs } from "dayjs";
import { useEffect, useState } from "react";

import { fetchAudit, type AuditRow } from "../../api/admin";
import {
  clearAudit,
  deleteAuditEntries,
  type DeletedCountResult,
} from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { LoadError } from "../shared/PageStates";
import { applyListFilters } from "../shared/listFilters";

const { Text } = Typography;

interface AuditFilters {
  since?: string;
  until?: string;
}

interface AuditFormValues {
  since?: Dayjs | null;
  until?: Dayjs | null;
}

function dateText(value?: Dayjs | null): string | undefined {
  return value?.format("YYYY-MM-DD");
}

function jsonText(value: unknown): string {
  if (value === null || value === undefined) {
    return "";
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function AuditPage() {
  const [form] = Form.useForm<AuditFormValues>();
  const [filters, setFilters] = useState<AuditFilters>({});
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selectedIDs, setSelectedIDs] = useState<number[]>([]);
  const confirm = useConfirmAction();

  const removeMany = useAdminAction<DeletedCountResult, number[]>({
    action: (ids) => deleteAuditEntries(ids),
    invalidate: [["audit"]],
    successText: (data) => `已删除 ${data.deleted} 条审计记录，并保留本次清理审计。`,
    onDone: () => {
      setSelectedIDs([]);
      setPage(1);
    },
  });
  const clearAll = useAdminAction<DeletedCountResult, undefined>({
    action: () => clearAudit(),
    invalidate: [["audit"]],
    successText: (data) => `已清除 ${data.deleted} 条历史审计，并保留本次清理记录。`,
    onDone: () => {
      setSelectedIDs([]);
      setPage(1);
    },
  });

  useEffect(() => {
    setSelectedIDs([]);
  }, [filters, page, pageSize]);

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["audit", "list", { ...filters, page, pageSize }],
    queryFn: () => fetchAudit({ ...filters, page, page_size: pageSize }),
  });

  const columns: ColumnsType<AuditRow> = [
    { title: "时间", dataIndex: "created_at", key: "created_at", render: fmtTime },
    { title: "操作者", dataIndex: "actor", key: "actor" },
    {
      title: "动作",
      dataIndex: "action",
      key: "action",
      render: (action: string) => <Text code>{action}</Text>,
    },
    {
      title: "对象",
      dataIndex: "target",
      key: "target",
      render: (target: string) => (target === "" ? "—" : target),
    },
  ];

  return (
    <PageScaffold
      title="审计日志"
      description="管理员写操作按时间倒序记录；批量删除或清除全部都会留下新的清理审计。"
      actions={
        <Button
          danger
          loading={clearAll.pending}
          disabled={removeMany.pending}
          onClick={() => {
            confirm({
              intent: "danger",
              title: "确认清除全部审计日志",
              content:
                "确定清除全部历史审计日志？此操作不受当前时间筛选影响，删除后不可恢复。系统将保留一条本次清理操作的审计记录。",
              action: () => clearAll.run(undefined),
            });
          }}
        >
          清除全部审计日志
        </Button>
      }
    >
      <PageSection>
        <Space direction="vertical" size="middle" className="field-width-full">
          <FilterBar<AuditFormValues>
            mode="submit"
            form={form}
            onFinish={(values) => {
              applyListFilters(
                { since: dateText(values.since), until: dateText(values.until) },
                filters,
                page,
                setPage,
                setFilters,
                refetch,
              );
            }}
            onReset={() => {
              applyListFilters({}, filters, page, setPage, setFilters, refetch);
            }}
          >
            <Form.Item name="since">
              <DatePicker placeholder="开始日期" maxDate={dayjs()} />
            </Form.Item>
            <Form.Item name="until">
              <DatePicker placeholder="结束日期" maxDate={dayjs()} />
            </Form.Item>
          </FilterBar>

          {selectedIDs.length > 0 ? (
            <ResponsiveActionBar align="start" className="batch-action-bar">
              <Button
                danger
                loading={removeMany.pending}
                disabled={clearAll.pending}
                onClick={() => {
                  const idsSnapshot = [...selectedIDs];
                  confirm({
                    intent: "danger",
                    title: "确认删除选中的审计记录",
                    content: `确定删除当前页明确选中的 ${idsSnapshot.length} 条审计记录？删除后不可恢复，系统会保留一条本次清理审计。`,
                    action: () => removeMany.run(idsSnapshot),
                  });
                }}
              >
                删除已选（{selectedIDs.length}）
              </Button>
              <Text type="secondary">仅删除当前页明确勾选的记录，不扩展到其他页或筛选结果。</Text>
            </ResponsiveActionBar>
          ) : null}

          {isError ? (
            <LoadError onRetry={() => void refetch()} />
          ) : (
            <DataTable<AuditRow>
              rowKey="id"
              rowSelection={{
                selectedRowKeys: selectedIDs,
                onChange: (keys) => {
                  // 批量删除提交期间冻结 selection：提交的是确认瞬间的快照，
                  // 勾选状态在 pending 结束前不可再变化。
                  if (!removeMany.pending) setSelectedIDs(keys.map((key) => Number(key)));
                },
                getCheckboxProps: () => ({ disabled: removeMany.pending }),
              }}
              loading={isPending}
              columns={columns}
              dataSource={data?.items}
              emptyText="暂无审计记录。"
              expandable={{
                expandedRowRender: (row) => {
                  const before = jsonText(row.before);
                  const after = jsonText(row.after);
                  return (
                    <div className="audit-detail">
                      {before !== "" && (
                        <div>
                          <Text type="secondary">变更前</Text>
                          <pre className="audit-json">{before}</pre>
                        </div>
                      )}
                      {after !== "" && (
                        <div>
                          <Text type="secondary">变更后</Text>
                          <pre className="audit-json">{after}</pre>
                        </div>
                      )}
                    </div>
                  );
                },
                rowExpandable: (row) => row.before !== null || row.after !== null,
              }}
              pagination={{
                current: data?.page ?? page,
                pageSize: data?.page_size ?? pageSize,
                total: data?.total ?? 0,
                onChange: (nextPage, nextSize) => {
                  setSelectedIDs([]);
                  setPage(nextPage);
                  setPageSize(nextSize);
                },
              }}
            />
          )}
          <Text type="secondary">
            审计记录按时间倒序展示；管理员可清理应用内历史，但每次批量删除或清除全部都会留下新的清理审计。
          </Text>
        </Space>
      </PageSection>
    </PageScaffold>
  );
}
