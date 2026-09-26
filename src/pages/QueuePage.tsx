import { AppLayout } from "@/components/layout/AppLayout";
import { Helmet } from "react-helmet-async";
import {
  ClipboardList,
  RefreshCw,
  Trash2,
  PauseCircle,
  PlayCircle,
  X,
  Clock,
  Plus,
  Loader2,
  Pencil,
  MapPin,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Chip } from "@/components/common/Chip";
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
  useQueueList,
  useToggleQueueItem,
  useRemoveQueueItem,
  useClearQueue,
  useCreateQueueItem,
  useUpdateQueueItem,
  usePurchaseTimings,
  type QueueItem,
  type QueueBatchResult,
  type PurchaseTiming,
} from "@/hooks/use-queue";
import { useServers } from "@/hooks/use-servers";
import { OVH_DATACENTERS as OVH_DC_LIST } from "@/lib/datacenters";
import { AccountSelect } from "@/components/common/AccountSelect";
import { useAccounts } from "@/hooks/use-accounts";
import { isValidQueueBatch, MAX_QUEUE_BATCH_TASKS, mergeQueueOptions, resolveImportedQueueOptions } from "@/lib/purchase-guards";
import { AccountChip } from "@/components/common/AccountChip";
import { TimingChip } from "@/components/common/TimingChip";
import { PlanCodeCombobox } from "@/components/common/PlanCodeCombobox";
import { OptionGroupSection } from "@/components/common/OptionGroupSection";
import { groupOptions, type OptionGroupKey } from "@/lib/option-groups";
import {
  useAvailability,
  buildVariantIndex,
  hasStockWithOption,
  variantDcStatus,
} from "@/hooks/use-availability";

/** 抢购队列：列表 + 暂停/恢复/删除/清空 + 新建抢购任务 */
/** OVH 数据中心列表：复用 lib/datacenters.ts 的共享常量 */
const OVH_DATACENTERS = OVH_DC_LIST;

/** 任务重试间隔默认值（秒），与后端 TASK_RETRY_INTERVAL 保持一致 */
const DEFAULT_RETRY_INTERVAL = 60;

function displayDatacenterCode(value: string): string {
  const normalized = value.trim().toLowerCase();
  return OVH_DATACENTERS.find((dc) => dc.code === normalized || dc.apiCode === normalized)?.code || normalized;
}

function QueuePage() {
  const queue = useQueueList();
  const timings = usePurchaseTimings();
  const toggle = useToggleQueueItem();
  const remove = useRemoveQueueItem();
  const deleteInFlight = useRef(false);
  const clear = useClearQueue();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const createPlanCode = searchParams.get("create") || undefined;
  const createOptions = searchParams.get("options") || undefined;
  const [showClearDialog, setShowClearDialog] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<QueueItem | null>(null);
  const [showCreateDialog, setShowCreateDialog] = useState(false);
  const [editingItem, setEditingItem] = useState<QueueItem | null>(null);
  const [prefillPlanCode, setPrefillPlanCode] = useState<string>("");
  const [prefillOptions, setPrefillOptions] = useState<string>("");

  // 从其它页跳到 /queue?create=KS-A-1&options=...，自动打开新建对话框并预填
  useEffect(() => {
    if (createPlanCode) {
      setPrefillPlanCode(createPlanCode);
      setPrefillOptions(createOptions || "");
      setShowCreateDialog(true);
    }
  }, [createPlanCode, createOptions]);

  const items = queue.data || [];

  return (
    <div className="space-y-6">
      <PageHeader
        icon={ClipboardList}
        title="抢购队列"
        description="管理自动抢购服务器的队列"
        action={
          <div className="flex gap-2">
            <Button onClick={() => setShowCreateDialog(true)}>
              <Plus className="w-4 h-4" />
              新建抢购任务
            </Button>
            <Button variant="outline" onClick={() => queue.refetch()} disabled={queue.isFetching}>
              <RefreshCw className={`w-4 h-4 ${queue.isFetching ? "animate-spin" : ""}`} />
              刷新
            </Button>
            <Button
              variant="outline"
              onClick={() => setShowClearDialog(true)}
              disabled={items.length === 0}
            >
              <Trash2 className="w-4 h-4" />
              清空
            </Button>
          </div>
        }
      />

      {queue.isError && (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-destructive/40 p-3 text-sm">
          <span>{queue.data ? "队列刷新失败，正在显示上次数据" : "队列加载失败"}</span>
          <Button variant="outline" size="sm" onClick={() => void queue.refetch()} disabled={queue.isFetching}>
            <RefreshCw className="w-4 h-4" />重试
          </Button>
        </div>
      )}
      {queue.isError && !queue.data ? null : queue.isPending ? (
        <div className="space-y-3">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-20 rounded-2xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <Card>
          <EmptyState
            icon={ClipboardList}
            title="暂无任务"
            description="点击右上角“新建抢购任务”开始抢购"
          />
        </Card>
      ) : (
        <div className="space-y-3">
          {items.map((q) => (
            <QueueRow
              key={q.id}
              item={q}
              timing={timings.data?.[`${q.planCode}@${q.datacenter}`]}
              onToggle={() =>
                toggle.mutate({
                  id: q.id,
                  action: q.status === "running" ? "pause" : "resume",
                })
              }
              onDelete={() => setDeleteTarget(q)}
              deletePending={remove.isPending}
              onEdit={() => setEditingItem(q)}
            />
          ))}
        </div>
      )}

      <Dialog open={showClearDialog} onOpenChange={setShowClearDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认清空队列？</DialogTitle>
            <DialogDescription>所有任务将被删除，此操作不可撤销。</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowClearDialog(false)}>
              取消
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                clear.mutate();
                setShowClearDialog(false);
              }}
            >
              确认清空
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={!!deleteTarget} onOpenChange={(next) => !next && !remove.isPending && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>删除并停止任务？</DialogTitle>
            <DialogDescription>
              {deleteTarget?.planCode} · {deleteTarget?.datacenter.toUpperCase()} 将立即停止并从队列移除，此操作不可撤销。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={remove.isPending}>取消</Button>
            <Button
              variant="destructive"
              disabled={remove.isPending || !deleteTarget}
              onClick={async () => {
                if (!deleteTarget || deleteInFlight.current || remove.isPending) return;
                deleteInFlight.current = true;
                try {
                  await remove.mutateAsync(deleteTarget.id);
                  setDeleteTarget(null);
                } catch {
                  // 删除 hook 已报告错误，保留确认目标供用户重试。
                } finally {
                  deleteInFlight.current = false;
                }
              }}
            >
              {remove.isPending ? "删除中…" : "确认删除"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <QueueEditDialog item={editingItem} open={!!editingItem} onOpenChange={(open) => !open && setEditingItem(null)} />

      <CreateQueueDialog
        open={showCreateDialog}
        onOpenChange={(v) => {
          setShowCreateDialog(v);
          if (!v) {
            setPrefillPlanCode("");
            setPrefillOptions("");
            // 清掉 URL 上的 create / options 参数
            navigate("/queue", { replace: true });
          }
        }}
        initialPlanCode={prefillPlanCode}
        initialOptions={prefillOptions}
      />
    </div>
  );
}

/** 创建抢购任务对话框 */
function CreateQueueDialog({
  open,
  onOpenChange,
  initialPlanCode,
  initialOptions,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  initialPlanCode?: string;
  initialOptions?: string;
}) {
  const servers = useServers();
  const accounts = useAccounts();
  const availQ = useAvailability();
  const variantIndex = useMemo(() => buildVariantIndex(availQ.data), [availQ.data]);
  const create = useCreateQueueItem();
  const submitInFlight = useRef(false);
  const [batchResult, setBatchResult] = useState<QueueBatchResult | null>(null);
  const [accountId, setAccountId] = useState("");
  const [planCode, setPlanCode] = useState(initialPlanCode || "");
  const [datacenters, setDatacenters] = useState<string[]>([]);
  const [quantity, setQuantity] = useState("1");
  const [retryInterval, setRetryInterval] = useState(String(DEFAULT_RETRY_INTERVAL));
  // 用户选的 addon,按组索引。每次切 planCode 自动清空(让用户重新选)。
  const [picked, setPicked] = useState<Partial<Record<OptionGroupKey, string>>>({});
  // 手填的额外 addon planCode(catalog 里没分组覆盖到的、或用户想加的特殊 addon)
  const [extraInput, setExtraInput] = useState("");

  useEffect(() => {
    if (open) {
      if (initialPlanCode) setPlanCode(initialPlanCode);
    }
  }, [initialPlanCode, open]);

  /** planCode 匹配到的服务器（用于显示名称提示） */
  const matchedServer = useMemo(
    () => (servers.data || []).find((s) => s.planCode === planCode.trim()),
    [servers.data, planCode]
  );

  // 切 planCode 清空 picked —— 之前选的 addon 对新机型多半不适用。
  // initialOptions 由外部传入时(从其它入口"快速添加"过来),解析后塞进 picked 让用户能看到。
  const prevPlanCodeRef = useRef("");
  const prevImportedOptionsRef = useRef("");
  useEffect(() => {
    if (!open) {
      prevPlanCodeRef.current = "";
      prevImportedOptionsRef.current = "";
      return;
    }
    const code = planCode.trim();
    const importedOptions = code === (initialPlanCode || "").trim() ? initialOptions || "" : "";
    if (importedOptions && !servers.isSuccess) return;
    if (code === prevPlanCodeRef.current && importedOptions === prevImportedOptionsRef.current) return;
    prevPlanCodeRef.current = code;
    prevImportedOptionsRef.current = importedOptions;
    if (importedOptions) {
      const groupedMap = matchedServer ? groupOptions(matchedServer.availableOptions) : null;
      const resolved = resolveImportedQueueOptions(importedOptions, groupedMap);
      setPicked(resolved.picked);
      setExtraInput(resolved.extras.join(", "));
    } else {
      setPicked({});
      setExtraInput("");
    }
  }, [open, planCode, initialOptions, initialPlanCode, matchedServer, servers.isSuccess]);

  /** 按组拆分该机型的所有可选 addon */
  const grouped = useMemo(
    () => (matchedServer ? groupOptions(matchedServer.availableOptions) : null),
    [matchedServer]
  );
  const defaultValueSet = useMemo(
    () => new Set((matchedServer?.defaultOptions || []).map((o) => o.value)),
    [matchedServer]
  );

  /** 分组选配和目录未覆盖的 addon 一并提交。 */
  const parsedOptions = useMemo(() => mergeQueueOptions(picked, extraInput), [picked, extraInput]);

  // option chip 的绿/红点:跟服务器列表对话框同一套逻辑
  const variants = matchedServer ? variantIndex[matchedServer.planCode] : undefined;
  const optionHasStock = (groupKey: OptionGroupKey, value: string): boolean => {
    if (groupKey === "bandwidth" || groupKey === "vrack" || groupKey === "cpu" || groupKey === "other") {
      return true;
    }
    return hasStockWithOption(
      variants,
      picked as Record<string, string>,
      groupKey,
      value,
      datacenters.length > 0 ? datacenters : undefined
    );
  };

  const qty = Number(quantity);
  const validBatch = quantity.trim() !== "" && isValidQueueBatch(qty, datacenters.length);
  const totalTasks = validBatch ? datacenters.length * qty : 0;
  const waitingForImport = !!initialOptions && planCode.trim() === (initialPlanCode || "").trim() && !servers.isSuccess;
  const canSubmit = !!accountId && planCode.trim().length > 0 && validBatch && !waitingForImport && !batchResult;

  const reset = () => {
    setPlanCode("");
    setDatacenters([]);
    setQuantity("1");
    setRetryInterval(String(DEFAULT_RETRY_INTERVAL));
    setPicked({});
    setExtraInput("");
    setBatchResult(null);
    prevPlanCodeRef.current = "";
    prevImportedOptionsRef.current = "";
  };

  const handleClose = () => {
    if (create.isPending) return;
    setBatchResult(null);
    onOpenChange(false);
  };

  const toggleDC = (code: string) => {
    setDatacenters((prev) =>
      prev.includes(code) ? prev.filter((c) => c !== code) : [...prev, code]
    );
  };

  const selectAllDC = () => setDatacenters(OVH_DATACENTERS.map((d) => d.code));
  const clearAllDC = () => setDatacenters([]);

  const handleSubmit = async () => {
    if (submitInFlight.current || create.isPending) return;
    if (!validBatch) {
      toast.error(`数量必须为正安全整数，且本批次不能超过 ${MAX_QUEUE_BATCH_TASKS} 个任务`);
      return;
    }
    if (!canSubmit) {
      toast.error(waitingForImport ? "服务器目录尚未加载，请等待或重试" : "请填写账户、计划代码并选择数据中心");
      return;
    }
    const selectedAccount = accounts.data?.find((account) => account.id === accountId);
    if (!selectedAccount) {
      toast.error("无法确认下单账户，请重试加载账户列表");
      return;
    }
    if (!window.confirm(`用账户 ${selectedAccount.name}（${selectedAccount.zone || selectedAccount.endpoint}）创建 ${totalTasks} 个自动抢购任务？每个成功创建的任务将立即启动。`)) return;
    submitInFlight.current = true;
    try {
      const result = await create.mutateAsync({
        account_id: accountId,
        planCode: planCode.trim(),
        datacenters,
        quantity: qty,
        retryInterval: Number(retryInterval) || DEFAULT_RETRY_INTERVAL,
        options: parsedOptions,
      });
      if (result.success === result.total) {
        toast.success(`已创建 ${result.success} 个抢购任务`);
        reset();
        onOpenChange(false);
        return;
      }
      setBatchResult(result);
      toast.error(`已创建 ${result.success}/${result.total} 个任务，${result.failed} 个失败；请查看明细，不要直接重提整批`);
    } catch {
      // 校验失败或请求异常已由 mutation 通知，保留输入供用户修正。
    } finally {
      submitInFlight.current = false;
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="w-[95vw] sm:w-full sm:max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>新建抢购任务</DialogTitle>
          <DialogDescription>
            为每个数据中心创建指定数量的独立任务，每台服务器单独成单。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5 py-2">
          {/* OVH 账户 */}
          <div>
            <label className="block text-[13px] font-medium mb-1.5">OVH 账户 *</label>
            <AccountSelect value={accountId} onChange={setAccountId} />
            <p className="text-[11px] text-muted-foreground mt-1">下单时用该账户的凭据,购物车 subsidiary 跟随账户 zone</p>
          </div>

          {/* 服务器计划代码 */}
          <div>
            <label className="block text-[13px] font-medium mb-1.5">服务器计划代码</label>
            <PlanCodeCombobox
              value={planCode}
              onChange={setPlanCode}
              servers={servers.data || []}
              placeholder="选择或搜索服务器型号"
            />
            {waitingForImport && (
              <div role={servers.isError ? "alert" : "status"} className="flex flex-wrap items-center gap-2 mt-2 text-[12px] text-muted-foreground">
                <span>{servers.isError ? "服务器目录加载失败，无法核对导入选配" : "正在读取服务器目录并核对导入选配…"}</span>
                {servers.isError && (
                  <Button type="button" size="sm" variant="outline" onClick={() => void servers.refetch()} disabled={servers.isFetching}>
                    <RefreshCw className="w-4 h-4" />重试目录
                  </Button>
                )}
              </div>
            )}
            {matchedServer && (
              <p className="text-[11px] text-muted-foreground mt-1 truncate">
                {matchedServer.cpu} · {matchedServer.memory} · {matchedServer.storage}
              </p>
            )}
          </div>

          {/* 数据中心多选 */}
          <div>
            <div className="flex items-center justify-between mb-1.5">
              <label className="block text-[13px] font-medium">
                选择数据中心
                {datacenters.length > 0 && (
                  <span className="text-muted-foreground ml-2 font-normal">
                    （已选 {datacenters.length}）
                  </span>
                )}
              </label>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={selectAllDC}
                  className="text-[11px] text-muted-foreground hover:text-foreground transition-colors"
                >
                  全选
                </button>
                <span className="text-muted-foreground text-[11px]">/</span>
                <button
                  type="button"
                  onClick={clearAllDC}
                  className="text-[11px] text-muted-foreground hover:text-foreground transition-colors"
                >
                  清空
                </button>
              </div>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-2 border border-border rounded-2xl p-3 max-h-56 overflow-y-auto">
              {OVH_DATACENTERS.map((dc) => {
                const checked = datacenters.includes(dc.code);
                return (
                  <label
                    key={dc.code}
                    className="flex items-center gap-2 cursor-pointer text-[13px] py-1"
                  >
                    <Checkbox
                      checked={checked}
                      onCheckedChange={() => toggleDC(dc.code)}
                    />
                    <span className="truncate" title={`${dc.name} (${dc.code})`}>
                      <span className="font-mono uppercase">{dc.code}</span>
                      <span className="text-muted-foreground ml-1">{dc.name}</span>
                    </span>
                  </label>
                );
              })}
            </div>
          </div>

          {/* 数量 + 重试间隔 */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label className="block text-[13px] font-medium mb-1.5">
                每个数据中心数量
              </label>
              <Input
                type="text"
                inputMode="numeric"
                value={quantity}
                onChange={(e) => {
                  const v = e.target.value;
                  if (v === "" || /^\d*$/.test(v)) setQuantity(v);
                }}
                placeholder="默认: 1"
              />
              <p className="text-[11px] text-muted-foreground mt-1">
                每台服务器单独成单；单批最多 {MAX_QUEUE_BATCH_TASKS} 个任务
              </p>
              {datacenters.length > 0 && !validBatch && (
                <p role="alert" className="text-[11px] text-destructive mt-1">数量须为正整数，机房数 × 数量不得超过 {MAX_QUEUE_BATCH_TASKS}</p>
              )}
            </div>
            <div>
              <label className="block text-[13px] font-medium mb-1.5">
                重试间隔（秒）
              </label>
              <Input
                type="text"
                inputMode="numeric"
                value={retryInterval}
                onChange={(e) => {
                  const v = e.target.value;
                  if (v === "" || /^\d*$/.test(v)) setRetryInterval(v);
                }}
                placeholder={`默认: ${DEFAULT_RETRY_INTERVAL}`}
              />
              <p className="text-[11px] text-muted-foreground mt-1">
                抢购失败后等待秒数再重试
              </p>
            </div>
          </div>

          {/* 分组配置与目录未覆盖的额外 addon 同时保留。 */}
          <div>
            <label className="block text-[13px] font-medium mb-1.5">
              可选配置
              <span className="text-muted-foreground ml-2 font-normal">
                {grouped ? "（选择配置，留空走 OVH 默认下单）" : "（自定义型号可手填 addon planCode）"}
              </span>
            </label>
            {grouped && (
              <div className="space-y-4">
                {(["cpu", "memory", "systemStorage", "storage", "bandwidth", "vrack", "other"] as OptionGroupKey[])
                  .filter((g) => grouped[g].length > 0)
                  .map((g) => (
                    <OptionGroupSection
                      key={g}
                      groupKey={g}
                      options={grouped[g]}
                      picked={picked[g] || ""}
                      defaultValueSet={defaultValueSet}
                      hasStock={variants && variants.length > 0 ? (value) => optionHasStock(g, value) : undefined}
                      onPick={(value) =>
                        setPicked((p) => ({
                          ...p,
                          [g]: p[g] === value ? "" : value, // 再点一次取消选中
                        }))
                      }
                    />
                  ))}
              </div>
            )}
            <Input
              className="mt-3"
              aria-label="其他 addon planCode，逗号分隔"
              placeholder={grouped ? "其他 addon planCode，逗号分隔（可选）" : "addon planCode，逗号分隔"}
              value={extraInput}
              disabled={waitingForImport || create.isPending || !!batchResult}
              onChange={(e) => setExtraInput(e.target.value)}
            />

            {parsedOptions.length > 0 && (
              <div className="flex flex-wrap gap-1.5 mt-3 pt-3 border-t border-border">
                <span className="text-[11px] text-muted-foreground">已选:</span>
                {parsedOptions.map((opt, i) => (
                  <Chip key={`${opt}-${i}`} tone="default" className="font-mono">
                    {opt}
                  </Chip>
                ))}
              </div>
            )}
          </div>


          {/* 汇总提示 */}
          {validBatch && (
            <div className="border border-border rounded-2xl p-3 text-[12px] text-muted-foreground">
              将创建 <span className="font-semibold text-foreground">{totalTasks}</span> 个独立任务
              （{datacenters.length} 个数据中心 × {qty} 台
              {parsedOptions.length > 0 ? ` · 含 ${parsedOptions.length} 个可选配置` : ""}）
            </div>
          )}
          {batchResult && (
            <div role="alert" className="rounded-md border border-destructive/40 p-3 text-sm space-y-2">
              <p>本批已创建 {batchResult.success}/{batchResult.total} 个任务，其余 {batchResult.failed} 个失败。不要直接重复提交整个批次。</p>
              <ul className="list-disc pl-5 max-h-24 overflow-y-auto">
                {batchResult.failures.map((failure, index) => (
                  <li key={`${failure.datacenter}-${index}`}>{failure.datacenter.toUpperCase()}：{failure.reason}</li>
                ))}
              </ul>
              <Button type="button" variant="outline" size="sm" onClick={() => { reset(); onOpenChange(false); }}>关闭并检查队列</Button>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={handleClose} disabled={create.isPending}>
            取消
          </Button>
          <Button onClick={handleSubmit} disabled={!canSubmit || create.isPending || accounts.isPending || !!batchResult}>
            {create.isPending ? (
              <>
                <Loader2 className="w-4 h-4 animate-spin" />
                创建中...
              </>
            ) : (
              <>
                <Plus className="w-4 h-4" />
                {datacenters.length > 0 ? `创建 ${totalTasks} 个任务` : "创建任务"}
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function QueueEditDialog({
  item,
  open,
  onOpenChange,
}: {
  item: QueueItem | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const servers = useServers();
  const accounts = useAccounts();
  const availability = useAvailability();
  const variantIndex = useMemo(() => buildVariantIndex(availability.data), [availability.data]);
  const update = useUpdateQueueItem();
  const [accountId, setAccountId] = useState("");
  const [planCode, setPlanCode] = useState("");
  const [datacenters, setDatacenters] = useState<string[]>([]);
  const [quantity, setQuantity] = useState("1");
  const [retryInterval, setRetryInterval] = useState(String(DEFAULT_RETRY_INTERVAL));
  const [picked, setPicked] = useState<Partial<Record<OptionGroupKey, string>>>({});
  const [rawOptions, setRawOptions] = useState<string[]>([]);
  const appliedOptionsForItem = useRef<string | null>(null);

  useEffect(() => {
    if (!open || !item) return;
    setAccountId(item.accountId);
    setPlanCode(item.planCode);
    setDatacenters([displayDatacenterCode(item.datacenter)]);
    setQuantity("1");
    setRetryInterval(String(item.retryInterval || DEFAULT_RETRY_INTERVAL));
    setPicked({});
    setRawOptions(item.options || []);
    appliedOptionsForItem.current = null;
  }, [open, item]);

  const server = useMemo(() => (servers.data || []).find((s) => s.planCode === planCode), [servers.data, planCode]);
  const grouped = useMemo(() => groupOptions(server?.availableOptions || []), [server?.availableOptions]);
  const defaultValueSet = useMemo(
    () => new Set((server?.defaultOptions || []).map((option) => option.value)),
    [server?.defaultOptions]
  );
  const variants = server ? variantIndex[server.planCode] : undefined;
  const selectedOptions = useMemo(() => {
    if (server) {
      const fqnGroups: OptionGroupKey[] = ["memory", "systemStorage", "storage"];
      return fqnGroups.map((groupKey) => picked[groupKey]).filter(Boolean) as string[];
    }
    return rawOptions;
  }, [server, picked, rawOptions]);
  const staticDcMap = useMemo(() => {
    const map: Record<string, string> = {};
    for (const dc of server?.datacenters || []) map[dc.datacenter.toLowerCase()] = dc.availability;
    return map;
  }, [server?.datacenters]);
  const variantDcMap = useMemo(
    () => variantDcStatus(variants, selectedOptions),
    [variants, selectedOptions]
  );
  const dcMap = useMemo(() => ({ ...staticDcMap, ...variantDcMap }), [staticDcMap, variantDcMap]);
  const isAvailable = (status: string | undefined) => !!status && status !== "unavailable" && status !== "unknown";
  const availableDcCodes = useMemo(
    () => OVH_DATACENTERS.filter((dc) => isAvailable(dcMap[dc.code] || (dc.apiCode ? dcMap[dc.apiCode] : undefined))).map((dc) => dc.code),
    [dcMap]
  );
  const availableDcCount = availableDcCodes.length;
  const totalDatacenters = OVH_DATACENTERS.length;
  const optionHasStock = (groupKey: OptionGroupKey, value: string): boolean => {
    if (!variants || variants.length === 0) return true;
    if (groupKey === "bandwidth" || groupKey === "vrack" || groupKey === "cpu" || groupKey === "other") return true;
    return hasStockWithOption(
      variants,
      picked as Record<string, string>,
      groupKey,
      value,
      datacenters.length > 0 ? datacenters : undefined
    );
  };

  useEffect(() => {
    if (!open || !item || !server || server.planCode !== planCode || appliedOptionsForItem.current === item.id) return;
    const wanted = new Set(item.options || []);
    const consumed = new Set<string>();
    const next: Partial<Record<OptionGroupKey, string>> = {};
    (Object.keys(grouped) as OptionGroupKey[]).forEach((groupKey) => {
      const hit = grouped[groupKey].find((option) => wanted.has(option.value));
      if (hit) {
        next[groupKey] = hit.value;
        consumed.add(hit.value);
      }
    });
    setPicked(next);
    setRawOptions((item.options || []).filter((value) => !consumed.has(value)));
    appliedOptionsForItem.current = item.id;
  }, [open, item, server, planCode, grouped]);

  const toggleDC = (code: string) => {
    setDatacenters((current) => current.includes(code) ? current.filter((value) => value !== code) : [...current, code]);
  };

  const editQty = Number(quantity);
  const validEditBatch = quantity.trim() !== "" && isValidQueueBatch(editQty, datacenters.length);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!item || !accountId || !planCode.trim() || datacenters.length === 0) {
      toast.error("请填写账户、型号并至少选择一个数据中心");
      return;
    }
    if (!validEditBatch) {
      toast.error(`数量须为正整数，且机房数 × 数量不得超过 ${MAX_QUEUE_BATCH_TASKS}`);
      return;
    }
    const selectedAccount = accounts.data?.find((account) => account.id === accountId);
    if (!selectedAccount) {
      toast.error("无法确认下单账户，请重试加载账户列表");
      return;
    }
    const total = datacenters.length * editQty;
    if (total > 1 && !window.confirm(`用账户 ${selectedAccount.name}（${selectedAccount.zone || selectedAccount.endpoint}）将此任务扩展为 ${total} 个自动抢购任务？新增任务将立即启动。`)) return;
    const options = server
      ? [...(Object.values(picked).filter(Boolean) as string[]), ...rawOptions]
      : rawOptions;
    try {
      await update.mutateAsync({
        id: item.id,
        account_id: accountId,
        planCode: planCode.trim(),
        datacenters,
        quantity: editQty,
        retryInterval: Math.max(1, Number(retryInterval) || DEFAULT_RETRY_INTERVAL),
        options,
      });
      onOpenChange(false);
    } catch {
      // mutation 已提示错误，留在编辑表单供用户修正。
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[95vw] sm:max-w-3xl max-h-[90vh] overflow-hidden flex flex-col">
        <DialogHeader>
          <DialogTitle>修改抢购任务</DialogTitle>
          <DialogDescription>按照创建抢购任务的方式修改账户、配置组合、数据中心数量和重试间隔。</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <div className="space-y-5 overflow-y-auto pr-1">
            <div>
              <label className="block text-[13px] font-medium mb-1.5">OVH 账户 *</label>
              <AccountSelect value={accountId} onChange={setAccountId} />
            </div>
            <div>
              <label className="block text-[13px] font-medium mb-1.5">服务器计划代码 *</label>
              <PlanCodeCombobox value={planCode} onChange={(value) => { setPlanCode(value); setPicked({}); setRawOptions([]); appliedOptionsForItem.current = item?.id || null; }} servers={servers.data || []} />
            </div>
            <div className="space-y-4">
              {(["memory", "systemStorage", "storage", "bandwidth"] as OptionGroupKey[]).map((groupKey) => {
                const choices = grouped[groupKey];
                if (choices.length === 0) return null;
                return (
                  <OptionGroupSection
                    key={groupKey}
                    groupKey={groupKey}
                    options={choices}
                    picked={picked[groupKey] || ""}
                    defaultValueSet={defaultValueSet}
                    hasStock={variants && variants.length > 0 ? (value) => optionHasStock(groupKey, value) : undefined}
                    onPick={(value) => setPicked((current) => ({ ...current, [groupKey]: current[groupKey] === value ? "" : value }))}
                  />
                );
              })}
              {rawOptions.length > 0 && (
                <div className="flex flex-wrap gap-1.5 pt-1">
                  <span className="text-[11px] text-muted-foreground">其他配置:</span>
                  {rawOptions.map((value) => <Chip key={value} tone="default" className="font-mono">{value}</Chip>)}
                </div>
              )}
            </div>
            <div>
              <div className="flex items-center justify-between mb-2.5 gap-2 flex-wrap">
                <h3 className="text-[13px] font-semibold flex items-center gap-1.5">
                  <MapPin className="w-3.5 h-3.5 text-muted-foreground" />
                  数据中心 · 选 {datacenters.length} / {totalDatacenters}
                </h3>
                <div className="flex items-center gap-2">
                  <span className="text-[11px] text-muted-foreground">{availableDcCount}/{totalDatacenters} 可用 · {Math.round((availableDcCount / totalDatacenters) * 100)}%</span>
                  <Button type="button" variant="outline" size="sm" className="h-7 text-[11px]" onClick={() => setDatacenters(datacenters.length > 0 ? [] : availableDcCodes)}>
                    {datacenters.length > 0 ? "清空" : "选可用"}
                  </Button>
                </div>
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-1.5 sm:gap-2">
                {OVH_DATACENTERS.map((dc) => {
                  const selected = datacenters.includes(dc.code);
                  const status = dcMap[dc.code] || (dc.apiCode ? dcMap[dc.apiCode] : undefined);
                  const available = isAvailable(status);
                  return (
                    <button key={dc.code} type="button" onClick={() => toggleDC(dc.code)} className={"text-left border rounded-xl px-3 py-2 flex items-center justify-between transition-colors " + (selected ? "border-foreground bg-foreground text-background" : "border-border hover:bg-secondary/50")}>
                      <div className="min-w-0">
                        <div className="text-[12px] font-bold font-mono">{dc.code.toUpperCase()}</div>
                        <div className={"text-[10px] truncate " + (selected ? "text-background/70" : "text-muted-foreground")}>{dc.region} · {dc.name}</div>
                      </div>
                      <StatusDot tone={available ? "success" : "danger"} size="sm" pulse={available && !selected} />
                    </button>
                  );
                })}
              </div>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">每个数据中心数量</label>
                <Input type="number" min={1} max={MAX_QUEUE_BATCH_TASKS} step={1} value={quantity} onChange={(event) => setQuantity(event.target.value)} />
                {datacenters.length > 0 && !validEditBatch && (
                  <p role="alert" className="text-[11px] text-destructive mt-1">数量须为正整数，单批最多 {MAX_QUEUE_BATCH_TASKS} 个任务</p>
                )}
              </div>
              <div><label className="block text-[11px] text-muted-foreground mb-1">重试间隔（秒）</label><Input type="number" min={1} value={retryInterval} onChange={(event) => setRetryInterval(event.target.value)} /></div>
            </div>
          </div>
          <DialogFooter className="mt-4 border-t border-border pt-4"><Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={update.isPending}>取消</Button><Button type="submit" disabled={!validEditBatch || !accountId || !planCode.trim() || accounts.isPending || update.isPending}>{update.isPending ? "保存中…" : "保存修改"}</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function QueueRow({
  item,
  timing,
  onToggle,
  onDelete,
  deletePending,
  onEdit,
}: {
  item: QueueItem;
  timing?: PurchaseTiming;
  onToggle: () => void;
  onDelete: () => void;
  deletePending: boolean;
  onEdit: () => void;
}) {
  const chip = (() => {
    if (item.status === "running")
      return (
        <Chip tone="success">
          <StatusDot tone="success" pulse size="xs" />运行中
        </Chip>
      );
    if (item.status === "pending")
      return (
        <Chip tone="warning">
          <StatusDot tone="warning" size="xs" />等待中
        </Chip>
      );
    if (item.status === "paused")
      return (
        <Chip tone="default">
          <StatusDot tone="muted" size="xs" />已暂停
        </Chip>
      );
    if (item.status === "completed")
      return (
        <Chip tone="info">
          <StatusDot tone="info" size="xs" />已完成
        </Chip>
      );
    return (
      <Chip tone="danger">
        <StatusDot tone="danger" size="xs" />失败
      </Chip>
    );
  })();

  return (
    <Card>
      <CardContent className="p-3 sm:p-5 flex flex-col sm:flex-row sm:items-center gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1 flex-wrap">
            <span className="font-mono font-semibold text-sm">{item.planCode}</span>
            {item.discontinued && (
              <Chip tone="danger">
                <StatusDot tone="danger" size="xs" />停售
              </Chip>
            )}
            <AccountChip accountId={item.accountId} />
            <TimingChip totalMs={timing?.totalMs} phases={timing?.phases} />
            <Chip tone="default">DC {item.datacenter.toUpperCase()}</Chip>
            {item.options && item.options.length > 0 && (
              <Chip tone="default">含 {item.options.length} 个可选配置</Chip>
            )}
          </div>
          <div className="text-[11px] text-muted-foreground flex items-center gap-2 flex-wrap">
            <Clock className="w-3 h-3" />
            <span>
              下次尝试 {item.discontinued ? "停售期间每小时检查" : item.retryCount > 0 ? `${item.retryInterval}秒后（第 ${item.retryCount + 1} 次）` : "即将开始"}
            </span>
            <span>·</span>
            <span>{new Date(item.createdAt).toLocaleString()}</span>
          </div>
        </div>
        <div className="flex items-center gap-2 flex-shrink-0">
          {chip}
          <Button variant="ghost" size="icon" onClick={onEdit} aria-label="编辑">
            <Pencil className="w-4 h-4" />
          </Button>
          {item.status !== "completed" && item.status !== "failed" && (
            <Button variant="ghost" size="icon" onClick={onToggle} aria-label={item.status === "running" ? "暂停" : "恢复"}>
              {item.status === "running" ? <PauseCircle className="w-4 h-4" /> : <PlayCircle className="w-4 h-4" />}
            </Button>
          )}
          <Button variant="ghost" size="icon" onClick={onDelete} disabled={deletePending}
            aria-label={`删除 ${item.planCode} ${item.datacenter.toUpperCase()} 任务`} title="删除任务">
            <X className="w-4 h-4" />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}


const Page = () => (
  <>
    <Helmet>
      <title>抢购队列 | OVH WebUI</title>
    </Helmet>
    <AppLayout>
      <QueuePage />
    </AppLayout>
  </>
);

export default Page;
