import { Timer } from "lucide-react";
import { toast } from "sonner";

export interface PhaseTiming {
  name: string;
  ms: number;
}

/** 仅显示总耗时，点击后查看已完成阶段明细；没有耗时数据时保持空白。 */
export function TimingChip({
  totalMs,
  phases,
  className,
}: {
  totalMs?: number;
  phases?: PhaseTiming[];
  className?: string;
}) {
  if (!totalMs || totalMs < 0) return null;
  const slowest = (phases || []).reduce<PhaseTiming | null>(
    (current, phase) => (!current || phase.ms > current.ms ? phase : current),
    null
  );
  const detail = () => {
    if (!phases?.length) {
      toast.info(`本轮抢购耗时 ${formatTimingMs(totalMs)}`);
      return;
    }
    toast.info(
      `总计 ${formatTimingMs(totalMs)}\n` + phases.map((phase) => `${phase.name} ${formatTimingMs(phase.ms)}`).join("\n"),
      { duration: 8000, style: { whiteSpace: "pre-line" } }
    );
  };
  return (
    <button
      type="button"
      onClick={detail}
      title={slowest ? `查看阶段耗时（最慢：${slowest.name} ${formatTimingMs(slowest.ms)}）` : "查看阶段耗时"}
      className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono bg-secondary text-muted-foreground hover:bg-muted transition-colors ${className || ""}`}
    >
      <Timer className="w-3 h-3" />
      {formatTimingMs(totalMs)}
    </button>
  );
}

export function formatTimingMs(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}
