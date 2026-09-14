/**
 * 已加入频道页：系统读取账号（userbot）当前加入的频道列表。
 * 列表来自 Telegram 对话遍历（较慢且有熔断副作用）：进入页面自动加载一次，
 * 之后由「刷新」手动触发；退出频道操作完成后自动重新拉取保持数据新鲜。
 * 类型/来源/关键词为客户端筛选，分页在客户端完成。
 * 创建者频道 Telegram 不允许退出。业务规则在 internal/joinmgr。
 */
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Input,
  Popconfirm,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import type { ColumnsType } from "antd/es/table";
import type { TableRowSelection } from "antd/es/table/interface";

import { fetchJoinedChannels, type JoinedChannelRow } from "../../api/admin";
import { leaveJoinedChannels } from "../../api/mutations";
import { fmtTime, JOIN_SOURCE_LABELS, joinSourceLabel } from "../../shared/format";
import { useAdminAction } from "../shared/actions";
import { useTitleMask } from "../shared/titleMask";
import { LoadError, PageCard } from "../shared/PageStates";

const { Text } = Typography;

const KIND_OPTIONS = [
  { value: "channel", label: "频道" },
  { value: "supergroup", label: "超级群组" },
];

const SOURCE_OPTIONS = Object.entries(JOIN_SOURCE_LABELS).map(([value, label]) => ({ value, label }));

export function JoinedChannelsPage() {
  const [selectedIDs, setSelectedIDs] = useState<number[]>([]);
  const [kindFilter, setKindFilter] = useState<string>("");
  const [sourceFilter, setSourceFilter] = useState<string>("");
  const [keyword, setKeyword] = useState<string>("");
  // 敏感频道名默认暗文（含 @用户名），顶部按钮一键显隐
  const { toggle: titleToggle, text: titleText } = useTitleMask();

  // 进入页面自动加载一次；之后由「刷新」或退出操作触发
  // （实时对话遍历较慢且带熔断副作用，不做窗口聚焦/定时自动刷新）。
  const joined = useQuery({
    queryKey: ["channel-join", "channels"],
    queryFn: fetchJoinedChannels,
  });

  const leave = useAdminAction({
    action: (ids: number[]) => leaveJoinedChannels(ids),
    invalidate: [["channel-join"]],
    successText: (result) => {
      const ok = result.outcomes.filter((o) => o.ok).length;
      const failed = result.outcomes.length - ok;
      return failed === 0
        ? `已退出 ${ok} 个频道。`
        : `已退出 ${ok} 个，${failed} 个失败（详见列表刷新后的状态）。`;
    },
    onDone: () => {
      setSelectedIDs([]);
      // 退出后自动刷新列表，保持展示与实际成员状态一致
      void joined.refetch();
    },
  });

  const joinedItems = joined.data?.items ?? [];
  const filteredItems = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    return joinedItems.filter((row) => {
      if (kindFilter && row.kind !== kindFilter) return false;
      if (sourceFilter && row.source !== sourceFilter) return false;
      if (kw) {
        const haystack = `${row.title} ${row.username}`.toLowerCase();
        if (!haystack.includes(kw)) return false;
      }
      return true;
    });
  }, [joinedItems, kindFilter, sourceFilter, keyword]);

  const creatorIDs = new Set(joinedItems.filter((row) => row.creator).map((row) => row.channel_id));
  const deletableSelected = selectedIDs.filter((id) => !creatorIDs.has(id));

  const columns: ColumnsType<JoinedChannelRow> = [
    {
      title: "频道",
      dataIndex: "title",
      render: (v: string) => titleText(v) || "（未知标题）",
    },
    {
      title: "用户名",
      dataIndex: "username",
      render: (v: string) => (v ? titleText(`@${v}`) : <Text type="secondary">—</Text>),
    },
    {
      title: "类型",
      dataIndex: "kind",
      render: (v: string) => (v === "channel" ? "频道" : "超级群组"),
    },
    {
      title: "来源",
      dataIndex: "source",
      render: (v: string) => {
        const label = joinSourceLabel(v);
        return v === "external" ? <Tag color="orange">{label}</Tag> : <Tag>{label}</Tag>;
      },
    },
    {
      title: "加入时间",
      dataIndex: "joined_at",
      render: (v: number) => (v ? fmtTime(v) : <Text type="secondary">未知</Text>),
    },
    {
      title: "操作",
      key: "leave",
      render: (_, row) =>
        row.creator ? (
          <Text type="secondary" title="账号是频道创建者，无法退出">
            创建者（不可退出）
          </Text>
        ) : (
          <Popconfirm
            title="确认退出该频道？"
            description="退出后该频道的消息链接将无法再提取。"
            okText="退出"
            cancelText="取消"
            onConfirm={() => void leave.run([row.channel_id])}
          >
            <Button size="small" danger loading={leave.pending}>
              退出
            </Button>
          </Popconfirm>
        ),
    },
  ];

  const rowSelection: TableRowSelection<JoinedChannelRow> = {
    selectedRowKeys: selectedIDs,
    onChange: (keys) => setSelectedIDs(keys.map(Number)),
    getCheckboxProps: (row) => ({ disabled: row.creator }),
  };

  const changeFilter = <T,>(setter: (v: T) => void) => (v: T) => {
    setter(v);
    setSelectedIDs([]); // 筛选变化时清空选择，避免对不可见行误操作
  };

  return (
    <PageCard
      title="已加入频道（实时）"
      extra={
        <Space>
          {titleToggle}
          <Button
            icon={<ReloadOutlined />}
            loading={joined.isFetching}
            onClick={() => void joined.refetch()}
          >
            刷新
          </Button>
          <Button
            danger
            disabled={deletableSelected.length === 0}
            loading={leave.pending}
            onClick={() => void leave.run(deletableSelected)}
          >
            批量退出（{deletableSelected.length}）
          </Button>
        </Space>
      }
    >
      <Space direction="vertical" size="small" className="field-width-full">
        <Alert
          type="info"
          showIcon
          message="列表来自 Telegram 用户号的实时对话，进入页面自动加载，退出频道后自动刷新；「外部拉入」表示非本系统加入的频道。开启「自动退出外部拉入」（受邀频道 → 受邀设置）后，每次刷新会自动退出这类频道；默认关闭。"

        />
        <Space wrap>
          <Select
            value={kindFilter}
            onChange={changeFilter(setKindFilter)}
            options={[{ value: "", label: "全部类型" }, ...KIND_OPTIONS]}
            className="field-width-130"
          />
          <Select
            value={sourceFilter}
            onChange={changeFilter(setSourceFilter)}
            options={[{ value: "", label: "全部来源" }, ...SOURCE_OPTIONS]}
            className="field-width-130"
          />
          <Input.Search
            placeholder="频道标题或用户名"
            allowClear
            value={keyword}
            onChange={(e) => changeFilter(setKeyword)(e.target.value)}
            onSearch={changeFilter(setKeyword)}
            className="field-width-200"
          />
        </Space>
        {joined.isError ? (
          <LoadError onRetry={() => void joined.refetch()} />
        ) : joined.isFetching ? (
          <Spin className="page-loading" tip="正在读取实时对话…" />
        ) : (
          <Table<JoinedChannelRow>
            rowKey="channel_id"
            size="small"
            columns={columns}
            dataSource={filteredItems}
            rowSelection={rowSelection}
            locale={{ emptyText: "没有符合条件的频道。" }}
            scroll={{ x: "max-content" }}
            pagination={{
              pageSize: 10,
              showSizeChanger: true,
              hideOnSinglePage: true,
              onChange: () => setSelectedIDs([]),
            }}
          />
        )}
      </Space>
    </PageCard>
  );
}
