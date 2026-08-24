import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Bell, HardDrive, MemoryStick, Network } from "lucide-react";
import { toast } from "sonner";
import { AccountSelect } from "@/components/common/AccountSelect";
import { PlanCodeCombobox } from "@/components/common/PlanCodeCombobox";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  type MonitorSubscription,
  useCreateMonitorSubscription,
  useMonitorList,
  useUpdateMonitorSubscription,
} from "@/hooks/use-monitor";
import { useServers, type ServerOption } from "@/hooks/use-servers";
import { OVH_DATACENTERS } from "@/lib/datacenters";
import { groupOptions } from "@/lib/option-groups";

type FilterChoice = { label: string; value: string };

function uniqueChoices(items: FilterChoice[]) {
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = item.value.trim().toLowerCase();
    if (!key || seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function optionChoices(options: ServerOption[]): FilterChoice[] {
  return options.map((option) => ({ label: option.label || option.value, value: option.value }));
}

function displayDatacenterCode(value: string): string {
  const normalized = value.trim().toLowerCase();
  return OVH_DATACENTERS.find((dc) => dc.code === normalized || dc.apiCode === normalized)?.code || normalized;
}

export function MonitorSubscriptionDialog({
  open,
  onOpenChange,
  subscription,
  initialPlanCode = "",
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  subscription?: MonitorSubscription | null;
  initialPlanCode?: string;
}) {
  const servers = useServers();
  const monitorList = useMonitorList();
  const create = useCreateMonitorSubscription();
  const update = useUpdateMonitorSubscription();
  const resolvedSubscription = subscription || monitorList.data?.find((item) => item.planCode === initialPlanCode);
  const editing = !!resolvedSubscription;
  const [planCode, setPlanCode] = useState("");
  const [datacenters, setDatacenters] = useState<string[]>([]);
  const [memories, setMemories] = useState<string[]>([]);
  const [storages, setStorages] = useState<string[]>([]);
  const [networks, setNetworks] = useState<string[]>([]);
  const [notifyAvailable, setNotifyAvailable] = useState(true);
  const [notifyUnavailable, setNotifyUnavailable] = useState(false);
  const [autoOrder, setAutoOrder] = useState(false);
  const [quantity, setQuantity] = useState(1);
  const [autoOrderAccountId, setAutoOrderAccountId] = useState("");

  useEffect(() => {
    if (!open) return;
    setPlanCode(resolvedSubscription?.planCode || initialPlanCode);
    setDatacenters((resolvedSubscription?.datacenters || []).map(displayDatacenterCode));
    setMemories(resolvedSubscription?.memories || []);
    setStorages(resolvedSubscription?.storages || []);
    setNetworks(resolvedSubscription?.networks || []);
    setNotifyAvailable(resolvedSubscription?.notifyAvailable ?? true);
    setNotifyUnavailable(resolvedSubscription?.notifyUnavailable ?? false);
    setAutoOrder(resolvedSubscription?.autoOrder ?? false);
    setQuantity(Math.max(1, resolvedSubscription?.quantity || 1));
    setAutoOrderAccountId(resolvedSubscription?.autoOrderAccountId || "");
  }, [open, resolvedSubscription, initialPlanCode]);

  const server = useMemo(
    () => (servers.data || []).find((item) => item.planCode === planCode),
    [servers.data, planCode]
  );
  const grouped = useMemo(() => groupOptions(server?.availableOptions || []), [server?.availableOptions]);
  const memoryChoices = useMemo(
    () => uniqueChoices([
      ...(server?.memory ? [{ label: `默认 · ${server.memory}`, value: server.memory }] : []),
      ...optionChoices(grouped.memory),
    ]),
    [server?.memory, grouped.memory]
  );
  const storageChoices = useMemo(
    () => uniqueChoices([
      ...(server?.storage ? [{ label: `默认 · ${server.storage}`, value: server.storage }] : []),
      ...optionChoices([...grouped.systemStorage, ...grouped.storage]),
    ]),
    [server?.storage, grouped.systemStorage, grouped.storage]
  );
  const networkChoices = useMemo(
    () => uniqueChoices([
      ...(server?.bandwidth ? [{ label: `默认 · ${server.bandwidth}`, value: server.bandwidth }] : []),
      ...optionChoices(grouped.bandwidth),
    ]),
    [server?.bandwidth, grouped.bandwidth]
  );

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const code = planCode.trim();
    if (!code) {
      toast.error("请选择服务器型号");
      return;
    }
    if (!notifyAvailable && !notifyUnavailable) {
      toast.error("请至少开启一种提醒");
      return;
    }
    if (autoOrder && !autoOrderAccountId) {
      toast.error("开启自动下单时必须选择 OVH 账户");
      return;
    }
    const payload = {
      planCode: code,
      datacenters,
      memories,
      storages,
      networks,
      notifyAvailable,
      notifyUnavailable,
      autoOrder,
      quantity: autoOrder ? quantity : undefined,
      autoOrderAccountId: autoOrder ? autoOrderAccountId : "",
    };
    if (editing) {
      await update.mutateAsync(payload);
    } else {
      await create.mutateAsync(payload);
    }
    onOpenChange(false);
  };

  const pending = create.isPending || update.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[95vw] sm:max-w-3xl max-h-[90vh] overflow-hidden flex flex-col">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑监控任务" : "添加监控任务"}</DialogTitle>
          <DialogDescription>内存、硬盘、网络和数据中心按组组合匹配；组内多选，组间同时满足。</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <div className="space-y-5 overflow-y-auto pr-1">
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1.5">服务器型号 *</label>
              {editing ? (
                <Input value={planCode} disabled className="font-mono" />
              ) : (
                <PlanCodeCombobox
                  value={planCode}
                  servers={servers.data || []}
                  onChange={(value) => {
                    setPlanCode(value);
                    setMemories([]);
                    setStorages([]);
                    setNetworks([]);
                  }}
                />
              )}
            </div>

            <FilterPicker icon={<MemoryStick className="h-4 w-4" />} label="内存" choices={memoryChoices} values={memories} onChange={setMemories} />
            <FilterPicker icon={<HardDrive className="h-4 w-4" />} label="硬盘" choices={storageChoices} values={storages} onChange={setStorages} />
            <FilterPicker icon={<Network className="h-4 w-4" />} label="网络" choices={networkChoices} values={networks} onChange={setNetworks} />

            <div>
              <div className="flex items-center justify-between gap-3 mb-2">
                <label className="text-xs font-medium text-muted-foreground">数据中心</label>
                <Button type="button" variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setDatacenters([])}>
                  清空（监控全部）
                </Button>
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 gap-2 rounded-2xl border border-border p-3">
                {OVH_DATACENTERS.map((dc) => {
                  const selected = datacenters.includes(dc.code);
                  return (
                    <label key={dc.code} className="flex items-center gap-2 cursor-pointer rounded-lg px-2 py-1.5 hover:bg-muted/50">
                      <Checkbox checked={selected} onCheckedChange={() => setDatacenters((current) => selected ? current.filter((code) => code !== dc.code) : [...current, dc.code])} />
                      <span className="text-xs"><span className="font-mono font-semibold">{dc.code.toUpperCase()}</span> · {dc.name}</span>
                    </label>
                  );
                })}
              </div>
              <p className="text-[11px] text-muted-foreground mt-1.5">未选择数据中心时监控全部机房。</p>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <ToggleCard checked={notifyAvailable} onChange={setNotifyAvailable} label="有货时提醒" />
              <ToggleCard checked={notifyUnavailable} onChange={setNotifyUnavailable} label="无货时提醒" />
              <ToggleCard checked={autoOrder} onChange={setAutoOrder} label="有货时自动下单" className="sm:col-span-2" />
            </div>

            {autoOrder && (
              <div className="grid grid-cols-1 sm:grid-cols-[1fr_160px] gap-3 rounded-2xl border border-border p-4">
                <div>
                  <label className="block text-xs font-medium text-muted-foreground mb-1.5">下单账户 *</label>
                  <AccountSelect value={autoOrderAccountId} onChange={setAutoOrderAccountId} />
                </div>
                <div>
                  <label className="block text-xs font-medium text-muted-foreground mb-1.5">每个匹配机房数量</label>
                  <Input type="number" min={1} max={100} value={quantity} onChange={(event) => setQuantity(Math.max(1, Number(event.target.value) || 1))} />
                </div>
              </div>
            )}
          </div>
          <DialogFooter className="mt-5 border-t border-border pt-4">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>取消</Button>
            <Button type="submit" disabled={pending}>
              <Bell className="h-4 w-4" />{pending ? "保存中…" : editing ? "保存修改" : "确认添加"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function FilterPicker({ icon, label, choices, values, onChange }: {
  icon: React.ReactNode;
  label: string;
  choices: FilterChoice[];
  values: string[];
  onChange: (values: string[]) => void;
}) {
  return (
    <div>
      <div className="flex items-center gap-2 mb-2 text-xs font-medium text-muted-foreground">{icon}{label}<span className="font-normal">（可多选，未选表示全部）</span></div>
      {choices.length > 0 ? (
        <div className="flex flex-wrap gap-2">
          {choices.map((choice) => {
            const selected = values.includes(choice.value);
            return (
              <button key={choice.value} type="button" onClick={() => onChange(selected ? values.filter((value) => value !== choice.value) : [...values, choice.value])}
                className={`rounded-full border px-3 py-1.5 text-xs transition-colors ${selected ? "border-primary bg-primary text-primary-foreground" : "border-border hover:bg-muted"}`}>
                {choice.label}
              </button>
            );
          })}
        </div>
      ) : (
        <p className="text-[11px] text-muted-foreground">选择目录中的服务器型号后可选择具体规格；当前按全部匹配。</p>
      )}
    </div>
  );
}

function ToggleCard({ checked, onChange, label, className = "" }: { checked: boolean; onChange: (value: boolean) => void; label: string; className?: string }) {
  return (
    <label className={`flex items-center gap-2.5 cursor-pointer rounded-xl border border-border px-3.5 py-2.5 hover:bg-muted/40 ${className}`}>
      <Checkbox checked={checked} onCheckedChange={(value) => onChange(!!value)} />
      <span className="text-sm">{label}</span>
    </label>
  );
}
