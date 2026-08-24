import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Bell, MapPin, ShoppingCart } from "lucide-react";
import { toast } from "sonner";
import { AccountSelect } from "@/components/common/AccountSelect";
import { PlanCodeCombobox } from "@/components/common/PlanCodeCombobox";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { OptionGroupSection } from "@/components/common/OptionGroupSection";
import { StatusDot } from "@/components/common/StatusDot";
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
import { OVH_DATACENTERS, lookupDcStatus } from "@/lib/datacenters";
import {
  type OptionGroupKey,
  groupOptions,
} from "@/lib/option-groups";
import {
  useAvailability,
  buildVariantIndex,
  hasStockWithOption,
  variantDcStatus,
} from "@/hooks/use-availability";

function mergeOptions(options: ServerOption[], defaults: ServerOption[]): ServerOption[] {
  const seen = new Set<string>();
  return [...options, ...defaults].filter((option) => {
    const value = option.value.trim().toLowerCase();
    if (!value || seen.has(value)) return false;
    seen.add(value);
    return true;
  });
}

function fallbackOption(value: string): ServerOption {
  return { label: value, value };
}

function isAvailableStatus(status: string | undefined): boolean {
  return !!status && status !== "unavailable" && status !== "unknown";
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
  const availability = useAvailability();
  const variantIndex = useMemo(() => buildVariantIndex(availability.data), [availability.data]);
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
    setMemories((resolvedSubscription?.memories || []).filter(Boolean).slice(0, 1));
    setStorages((resolvedSubscription?.storages || []).filter(Boolean).slice(0, 1));
    setNetworks((resolvedSubscription?.networks || []).filter(Boolean).slice(0, 1));
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
  const defaultValues = useMemo(
    () => {
      const values = new Set((server?.defaultOptions || []).map((option) => option.value));
      if ((server?.defaultOptions || []).length === 0) {
        if (server?.memory) values.add(server.memory);
        if (server?.storage) values.add(server.storage);
        if (server?.bandwidth) values.add(server.bandwidth);
      }
      return values;
    },
    [server?.defaultOptions, server?.memory, server?.storage, server?.bandwidth]
  );
  const groupedDefaults = useMemo(() => groupOptions(server?.defaultOptions || []), [server?.defaultOptions]);
  const memoryChoices = useMemo(
    () => {
      const options = mergeOptions(grouped.memory, groupedDefaults.memory);
      return options.length > 0
        ? options
        : server?.memory
          ? [fallbackOption(server.memory)]
          : [];
    },
    [server?.memory, grouped.memory, groupedDefaults.memory, defaultValues]
  );
  const storageChoices = useMemo(
    () => {
      const options = mergeOptions(
        [...grouped.systemStorage, ...grouped.storage],
        [...groupedDefaults.systemStorage, ...groupedDefaults.storage]
      );
      return options.length > 0
        ? options
        : server?.storage
          ? [fallbackOption(server.storage)]
          : [];
    },
    [server?.storage, grouped.systemStorage, grouped.storage, groupedDefaults.systemStorage, groupedDefaults.storage, defaultValues]
  );
  const networkChoices = useMemo(
    () => {
      const options = mergeOptions(grouped.bandwidth, groupedDefaults.bandwidth);
      return options.length > 0
        ? options
        : server?.bandwidth
          ? [fallbackOption(server.bandwidth)]
          : [];
    },
    [server?.bandwidth, grouped.bandwidth, groupedDefaults.bandwidth, defaultValues]
  );

  const variants = server ? variantIndex[server.planCode] : undefined;
  const selectedOptions = useMemo(
    () => [...memories, ...storages].filter(Boolean),
    [memories, storages]
  );
  const pickedForAvailability = useMemo(
    () => ({
      memory: memories[0] || "",
      storage: storages[0] || "",
      bandwidth: networks[0] || "",
    }),
    [memories, storages, networks]
  );
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
  const availableDcCount = useMemo(
    () => OVH_DATACENTERS.filter((dc) => isAvailableStatus(lookupDcStatus(dcMap, dc))).length,
    [dcMap]
  );
  const totalDatacenters = OVH_DATACENTERS.length;
  const availabilityRatio = totalDatacenters > 0 ? availableDcCount / totalDatacenters : 0;
  const optionHasStock = (groupKey: OptionGroupKey, value: string): boolean => {
    if (!variants || variants.length === 0) return true;
    if (groupKey === "bandwidth" || groupKey === "vrack" || groupKey === "cpu" || groupKey === "other") return true;
    return hasStockWithOption(
      variants,
      pickedForAvailability,
      groupKey,
      value,
      datacenters.length > 0 ? datacenters : undefined
    );
  };
  const toggleSingle = (setter: React.Dispatch<React.SetStateAction<string[]>>, value: string) => {
    setter((current) => (current[0] === value ? [] : [value]));
  };

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
          <DialogTitle>{editing ? "修改监控任务" : "创建监控任务"}</DialogTitle>
          <DialogDescription>按配置组合与数据中心监控库存；每个配置组可选择一项，未选择表示不限。</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <div className="space-y-5 overflow-y-auto pr-1">
            <div>
              <label className="block text-[13px] font-medium mb-1.5">服务器计划代码 *</label>
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

            <div className="space-y-4">
              {memoryChoices.length > 0 && (
                <OptionGroupSection
                  groupKey="memory"
                  options={memoryChoices}
                  picked={memories[0] || ""}
                  defaultValueSet={defaultValues}
                  hasStock={variants && variants.length > 0 ? (value) => optionHasStock("memory", value) : undefined}
                  onPick={(value) => toggleSingle(setMemories, value)}
                />
              )}
              {storageChoices.length > 0 && (
                <OptionGroupSection
                  groupKey="storage"
                  label="存储 / 数据盘"
                  options={storageChoices}
                  picked={storages[0] || ""}
                  defaultValueSet={defaultValues}
                  hasStock={variants && variants.length > 0 ? (value) => optionHasStock("storage", value) : undefined}
                  onPick={(value) => toggleSingle(setStorages, value)}
                />
              )}
              {networkChoices.length > 0 && (
                <OptionGroupSection
                  groupKey="bandwidth"
                  options={networkChoices}
                  picked={networks[0] || ""}
                  defaultValueSet={defaultValues}
                  hasStock={variants && variants.length > 0 ? (value) => optionHasStock("bandwidth", value) : undefined}
                  onPick={(value) => toggleSingle(setNetworks, value)}
                />
              )}
              {server && memoryChoices.length === 0 && storageChoices.length === 0 && networkChoices.length === 0 && (
                <p className="text-[11px] text-muted-foreground">该型号没有可选硬件配置，将按默认配置监控。</p>
              )}
            </div>

            <div>
              <div className="flex items-center justify-between mb-2.5 gap-2 flex-wrap">
                <h3 className="text-[13px] font-semibold flex items-center gap-1.5">
                  <MapPin className="w-3.5 h-3.5 text-muted-foreground" />
                  数据中心 · 选 {datacenters.length} / {totalDatacenters}
                </h3>
                <div className="flex items-center gap-2">
                  <span className="text-[11px] text-muted-foreground">
                    {availableDcCount}/{totalDatacenters} 可用 · {Math.round(availabilityRatio * 100)}%
                  </span>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 text-[11px]"
                    onClick={() => {
                      if (datacenters.length > 0) {
                        setDatacenters([]);
                        return;
                      }
                      setDatacenters(
                        OVH_DATACENTERS
                          .filter((dc) => isAvailableStatus(lookupDcStatus(dcMap, dc)))
                          .map((dc) => dc.code)
                      );
                    }}
                  >
                    {datacenters.length > 0 ? "清空" : "选可用"}
                  </Button>
                </div>
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-1.5 sm:gap-2">
                {OVH_DATACENTERS.map((dc) => {
                  const selected = datacenters.includes(dc.code);
                  const available = isAvailableStatus(lookupDcStatus(dcMap, dc));
                  return (
                    <button
                      key={dc.code}
                      type="button"
                      onClick={() => setDatacenters((current) => selected ? current.filter((code) => code !== dc.code) : [...current, dc.code])}
                      className={
                        "text-left border rounded-xl px-3 py-2 flex items-center justify-between transition-colors " +
                        (selected
                          ? "border-foreground bg-foreground text-background"
                          : "border-border hover:bg-secondary/50")
                      }
                    >
                      <div className="min-w-0">
                        <div className="text-[12px] font-bold font-mono">{dc.code.toUpperCase()}</div>
                        <div className={"text-[10px] truncate " + (selected ? "text-background/70" : "text-muted-foreground")}>
                          {dc.region} · {dc.name}
                        </div>
                      </div>
                      <StatusDot tone={available ? "success" : "danger"} size="sm" pulse={available && !selected} />
                    </button>
                  );
                })}
              </div>
              <p className="text-[11px] text-muted-foreground mt-1.5">未选择表示监控全部数据中心。</p>
            </div>

            <div className="border-t border-border pt-4">
              <h3 className="text-[13px] font-semibold mb-2.5 flex items-center gap-1.5">
                <Bell className="w-3.5 h-3.5 text-muted-foreground" />
                提醒方式
              </h3>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <ToggleCard checked={notifyAvailable} onChange={setNotifyAvailable} label="有货时提醒" />
                <ToggleCard checked={notifyUnavailable} onChange={setNotifyUnavailable} label="无货时提醒" />
                <ToggleCard checked={autoOrder} onChange={setAutoOrder} label="有货时自动下单" className="sm:col-span-2" />
              </div>
            </div>

            {autoOrder && (
              <div className="border-t border-border pt-4">
                <h3 className="text-[13px] font-semibold mb-2.5 flex items-center gap-1.5">
                  <ShoppingCart className="w-3.5 h-3.5 text-muted-foreground" />
                  抢购参数
                </h3>
                <div className="grid grid-cols-1 sm:grid-cols-[1fr_180px] gap-3">
                  <div>
                    <label className="block text-[11px] text-muted-foreground mb-1">OVH 账户 *</label>
                    <AccountSelect value={autoOrderAccountId} onChange={setAutoOrderAccountId} />
                  </div>
                  <div>
                    <label className="block text-[11px] text-muted-foreground mb-1">每个数据中心数量</label>
                    <Input type="number" min={1} max={100} value={quantity} onChange={(event) => setQuantity(Math.max(1, Number(event.target.value) || 1))} />
                  </div>
                </div>
              </div>
            )}
          </div>
          <DialogFooter className="mt-5 border-t border-border pt-4">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>取消</Button>
            <Button type="submit" disabled={pending}>
              <Bell className="h-4 w-4" />{pending ? "保存中…" : editing ? "保存修改" : "创建任务"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
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
