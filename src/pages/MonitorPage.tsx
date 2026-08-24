import { AppLayout } from "@/components/layout/AppLayout";
import { Helmet } from "react-helmet-async";
import {
  Bell,
  BellOff,
  RefreshCw,
  Trash2,
  X,
  History as HistoryIcon,
  ChevronUp,
  Plus,
  Pencil,
} from "lucide-react";
import { useState } from "react";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/common/Chip";
import { AccountChip } from "@/components/common/AccountChip";
import { StatusDot } from "@/components/common/StatusDot";
import { EmptyState } from "@/components/common/EmptyState";
import { Skeleton } from "@/components/common/Skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  useMonitorList,
  useMonitorStatus,
  useRemoveMonitorSubscription,
  useClearMonitor,
  useMonitorHistory,
  type MonitorSubscription,
} from "@/hooks/use-monitor";
import { MonitorSubscriptionDialog } from "@/components/common/MonitorSubscriptionDialog";

/** 服务器监控订阅 */
function MonitorPage() {
  const list = useMonitorList();
  const status = useMonitorStatus();
  const remove = useRemoveMonitorSubscription();
  const clear = useClearMonitor();
  const [confirmClear, setConfirmClear] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const [openAdd, setOpenAdd] = useState(false);
  const [editing, setEditing] = useState<MonitorSubscription | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const subs = list.data || [];

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Bell}
        title="服务器监控"
        description="自动监控服务器可用性变化并推送通知"
        action={
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => list.refetch()} disabled={list.isFetching}>
              <RefreshCw className={`w-4 h-4 ${list.isFetching ? "animate-spin" : ""}`} />
              刷新
            </Button>
            <Button onClick={() => setOpenAdd(true)}>
              <Plus className="w-4 h-4" />
              添加订阅
            </Button>
            <Button
              variant="outline"
              onClick={() => setConfirmClear(true)}
              disabled={subs.length === 0}
            >
              <Trash2 className="w-4 h-4" />
              清空全部
            </Button>
          </div>
        }
      />

      {/* 状态卡 */}
      <Card>
        <CardContent className="p-5 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-full bg-secondary flex items-center justify-center">
              {status.data?.running ? (
                <Bell className="w-5 h-5 text-success" />
              ) : (
                <BellOff className="w-5 h-5 text-muted-foreground" />
              )}
            </div>
            <div>
              <div className="text-sm font-semibold">监控状态</div>
              <div className="text-xs text-muted-foreground inline-flex items-center gap-1.5">
                <StatusDot
                  tone={status.data?.running ? "success" : "muted"}
                  pulse={status.data?.running}
                  size="xs"
                />
                {status.data?.running ? "运行中" : "已停止"}
              </div>
            </div>
          </div>
          <div className="flex gap-6 text-sm">
            <Stat label="订阅数" value={status.data?.subscriptions_count ?? 0} />
            <Stat label="检查间隔" value={`${status.data?.check_interval ?? 0}s`} />
            <Stat label="已知服务器" value={status.data?.known_servers_count ?? 0} />
          </div>
        </CardContent>
      </Card>

      {/* 订阅列表 */}
      {list.isPending ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
      ) : subs.length === 0 ? (
        <Card>
          <EmptyState
            icon={Bell}
            title="暂无订阅"
            description='点击"添加订阅"按钮开始监控服务器'
          />
        </Card>
      ) : (
        <div className="space-y-3">
          {subs.map((s) => (
            <SubRow
              key={s.planCode}
              sub={s}
              expanded={expanded === s.planCode}
              onToggleExpand={() =>
                setExpanded((curr) => (curr === s.planCode ? null : s.planCode))
              }
               onDelete={() => setConfirmRemove(s.planCode)}
               onEdit={() => setEditing(s)}
            />
          ))}
        </div>
      )}

      <MonitorSubscriptionDialog open={openAdd} onOpenChange={setOpenAdd} />
      <MonitorSubscriptionDialog open={!!editing} subscription={editing} onOpenChange={(open) => !open && setEditing(null)} />

      {/* 删除确认 */}
      <Dialog open={!!confirmRemove} onOpenChange={(v) => !v && setConfirmRemove(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>取消订阅</DialogTitle>
            <DialogDescription>
              确定要取消订阅 <span className="font-mono">{confirmRemove}</span> 吗？
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmRemove(null)}>
              取消
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                if (confirmRemove) remove.mutate(confirmRemove);
                setConfirmRemove(null);
              }}
            >
              确定
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 清空确认 */}
      <Dialog open={confirmClear} onOpenChange={setConfirmClear}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认清空所有订阅？</DialogTitle>
            <DialogDescription>所有监控订阅将被删除，此操作不可撤销。</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmClear(false)}>
              取消
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                clear.mutate();
                setConfirmClear(false);
              }}
            >
              确认清空
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/* ------------------------------ 行 / 历史 ------------------------------ */

function SubRow({
  sub,
  expanded,
  onToggleExpand,
  onDelete,
  onEdit,
}: {
  sub: MonitorSubscription;
  expanded: boolean;
  onToggleExpand: () => void;
  onDelete: () => void;
  onEdit: () => void;
}) {
  return (
    <Card>
      <CardContent className="p-5">
        <div className="flex flex-col sm:flex-row sm:items-center gap-3">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-1 flex-wrap">
              <span className="font-mono font-semibold text-sm">{sub.planCode}</span>
              {sub.serverName && (
                <span className="text-xs text-muted-foreground">| {sub.serverName}</span>
              )}
            </div>
            <p className="text-xs text-muted-foreground mb-1.5">
              {sub.datacenters.length > 0
                ? `监控数据中心: ${sub.datacenters.join(", ")}`
                : "监控所有数据中心"}
            </p>
            <div className="flex gap-1.5 flex-wrap items-center">
              {sub.memories?.length ? <Chip tone="default">内存: {sub.memories.join(" / ")}</Chip> : null}
              {sub.storages?.length ? <Chip tone="default">硬盘: {sub.storages.join(" / ")}</Chip> : null}
              {sub.networks?.length ? <Chip tone="default">网络: {sub.networks.join(" / ")}</Chip> : null}
              {sub.notifyAvailable && <Chip tone="success">有货提醒</Chip>}
              {sub.notifyUnavailable && <Chip tone="warning">无货提醒</Chip>}
              {sub.autoOrder && sub.autoOrderAccountId ? (
                <>
                  <Chip tone="solid">
                    自动下单
                    {sub.quantity && sub.quantity > 1 ? ` ×${sub.quantity}` : ""}
                  </Chip>
                  <span className="text-[11px] text-muted-foreground">→</span>
                  <AccountChip accountId={sub.autoOrderAccountId} />
                </>
              ) : sub.autoOrder ? (
                <Chip tone="warning">已勾自动下单但未选账户(只通知)</Chip>
              ) : null}
            </div>
          </div>
          <div className="flex items-center gap-1 flex-shrink-0">
            <Button
              variant="ghost"
              size="icon"
              aria-label="查看历史"
              onClick={onToggleExpand}
            >
              {expanded ? (
                <ChevronUp className="w-4 h-4" />
              ) : (
                <HistoryIcon className="w-4 h-4" />
              )}
            </Button>
            <Button variant="ghost" size="icon" onClick={onDelete} aria-label="删除">
              <X className="w-4 h-4" />
            </Button>
            <Button variant="ghost" size="icon" onClick={onEdit} aria-label="编辑">
              <Pencil className="w-4 h-4" />
            </Button>
          </div>
        </div>

        {expanded && (
          <div className="mt-4 pt-4 border-t border-border">
            <HistoryPanel planCode={sub.planCode} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function HistoryPanel({ planCode }: { planCode: string }) {
  const history = useMonitorHistory(planCode);

  if (history.isPending) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-10 rounded-xl" />
        ))}
      </div>
    );
  }

  const entries = history.data || [];

  return (
    <div>
      <div className="flex items-center gap-2 mb-3">
        <HistoryIcon className="w-4 h-4 text-muted-foreground" />
        <span className="text-sm font-medium">变化历史</span>
      </div>
      {entries.length === 0 ? (
        <p className="text-xs text-muted-foreground text-center py-4">暂无历史记录</p>
      ) : (
        <div className="space-y-2 max-h-64 overflow-y-auto">
          {entries.map((e, i) => (
            <div
              key={i}
              className="flex items-start gap-3 p-2.5 bg-muted/40 rounded-xl text-xs"
            >
              <StatusDot
                tone={e.changeType === "available" ? "success" : "danger"}
                size="sm"
                className="mt-1"
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-medium">{e.datacenter?.toUpperCase()}</span>
                  <Chip tone={e.changeType === "available" ? "success" : "danger"}>
                    {e.changeType === "available" ? "有货" : "无货"}
                  </Chip>
                  {e.config?.display && (
                    <span className="px-2 py-0.5 rounded-full bg-secondary text-[11px]">
                      {e.config.display}
                    </span>
                  )}
                </div>
                <p className="text-muted-foreground mt-1">{formatTime(e.timestamp)}</p>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function formatTime(ts: string): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(
    d.getHours()
  )}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function Stat({ label, value }: { label: string; value: number | string }) {
  return (
    <div>
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="text-lg font-semibold">{value}</div>
    </div>
  );
}


const Page = () => (
  <>
    <Helmet>
      <title>独服监控 | OVH WebUI</title>
    </Helmet>
    <AppLayout>
      <MonitorPage />
    </AppLayout>
  </>
);

export default Page;
