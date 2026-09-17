import { Table } from "antd";
import type { TableProps } from "antd";
import type { AnyObject } from "antd/es/_util/type";
import type { ReactNode } from "react";

import { TableQueryState } from "./QueryStates";

export interface DataTableProps<RecordType extends AnyObject = AnyObject>
  extends Omit<TableProps<RecordType>, "size"> {
  density?: "default" | "compact";
  emptyText?: ReactNode;
}

/** Ant Table with shared visual defaults; callers still own columns, data, queries and pagination state. */
export function DataTable<RecordType extends AnyObject = AnyObject>({
  density = "default",
  emptyText = "暂无数据。",
  scroll,
  locale,
  pagination,
  ...rest
}: DataTableProps<RecordType>) {
  const hasCallerEmptyText = locale && Object.hasOwn(locale, "emptyText");

  return (
    <Table<RecordType>
      {...rest}
      size={density === "compact" ? "small" : "middle"}
      scroll={{ x: "max-content", ...scroll }}
      locale={{
        ...locale,
        emptyText: hasCallerEmptyText ? locale.emptyText : (
          <TableQueryState state="empty" description={emptyText} />
        ),
      }}
      pagination={
        pagination === false
          ? false
          : {
              position: ["bottomRight"],
              showSizeChanger: true,
              ...pagination,
            }
      }
    />
  );
}
