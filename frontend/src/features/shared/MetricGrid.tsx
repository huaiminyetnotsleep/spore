/**
 * KPI 指标卡栅格（核心指标 / 全时段快照等模块共用）：全部指标单行等宽展示
 * （列数修饰类 metric-grid--N），卡片结构 = 顶部语义色条 → 标签 → 大数值 →
 * 可选进度条 → 可选多值行（stats，如 成功/失败/未完成 合并卡）→ 脚注。
 * 进度条用固定分段格子渲染（宽度是确定性的格子数，不用内联样式，符合 CSP 约定）。
 */
export interface MetricStat {
  label: string;
  value: string | number;
  /** 数值语义色：success 绿 / danger 红 / warning 橙；缺省继承正文色。 */
  tone?: "success" | "danger" | "warning";
}

export interface MetricItem {
  label: string;
  /** 大数值；缺省时不渲染数值行（纯多值卡，如 成功/失败/未完成 合并卡）。 */
  value?: string | number;
  /** 数值后的弱化后缀（如 " / 20"）。 */
  suffix?: string;
  /** 顶部色条语义：默认蓝 / success 绿 / danger 红 / warning 橙。 */
  tone?: "default" | "success" | "danger" | "warning";
  /** 0–1 的进度（如成功率/错误率）；越界或缺省不渲染进度条。 */
  progress?: number;
  /** 大数值下的多值行（小型“标签 + 数值”组合，等分铺满卡宽）。 */
  stats?: MetricStat[];
  /** 底部小字脚注。 */
  hint?: string;
}

/** 进度条分段数：格子数四舍五入，避免动态宽度样式。 */
const PROGRESS_CELLS = 12;

function progressCells(progress: number | undefined): number | null {
  if (progress === undefined || !Number.isFinite(progress)) return null;
  if (progress <= 0) return 0;
  if (progress >= 1) return PROGRESS_CELLS;
  return Math.round(progress * PROGRESS_CELLS);
}

export function MetricGrid({
  items,
  maxColumns,
}: {
  items: MetricItem[];
  /** 单行最大列数：超出的指标换行（缺省 = 单行铺满全部指标）。 */
  maxColumns?: number;
}) {
  const maxClass = maxColumns && maxColumns >= 1 && maxColumns <= 4 ? ` metric-grid--max-${maxColumns}` : "";
  return (
    <div className={`metric-grid metric-grid--${items.length}${maxClass}`}>
      {items.map((item) => {
        const filled = progressCells(item.progress);
        return (
          <div key={item.label} className={`metric-card metric-card--${item.tone ?? "default"}`}>
            <span className="metric-card__label">{item.label}</span>
            {item.value !== undefined ? (
              <div className="metric-card__value">
                {item.value}
                {item.suffix ? <span className="metric-card__suffix">{item.suffix}</span> : null}
              </div>
            ) : null}
            {filled !== null ? (
              <div
                className="metric-card__bar"
                role="progressbar"
                aria-label={item.label}
                aria-valuemin={0}
                aria-valuemax={PROGRESS_CELLS}
                aria-valuenow={filled}
              >
                {Array.from({ length: PROGRESS_CELLS }, (_, i) => (
                  <span key={i} className={`metric-card__bar-cell${i < filled ? " metric-card__bar-cell--on" : ""}`} />
                ))}
              </div>
            ) : null}
            {item.stats?.length ? (
              <div className={`metric-card__stats metric-card__stats--${item.stats.length}`}>
                {item.stats.map((stat) => (
                  <span key={stat.label} className={`metric-card__stat${stat.tone ? ` metric-card__stat--${stat.tone}` : ""}`}>
                    <span className="metric-card__stat-label">{stat.label}</span>
                    <span className="metric-card__stat-value">{stat.value}</span>
                  </span>
                ))}
              </div>
            ) : null}
            {item.hint ? <span className="metric-card__hint">{item.hint}</span> : null}
          </div>
        );
      })}
    </div>
  );
}
