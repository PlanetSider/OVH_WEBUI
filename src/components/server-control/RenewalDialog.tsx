import { useEffect, useState } from "react";
import { AlertCircle, Lock, Repeat } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { apiErrorText } from "@/lib/http";
import { useUpdateRenewal, useUpdateTerminationPolicy, type ServiceInfo } from "@/hooks/use-server-control";
import { toast } from "sonner";

type RenewMode = "auto" | "manual" | "delete";
export type TerminationPolicy = "empty" | "terminateAtExpirationDate" | "terminateAtEngagementDate";

const MODE_OPTIONS: Array<{ value: RenewMode; label: string; desc: string }> = [
  { value: "auto", label: "自动续费", desc: "到期前 OVH 自动扣款续费" },
  { value: "manual", label: "手动续费", desc: "到期前需手动付款，不付则服务终止" },
  { value: "delete", label: "到期终止", desc: "到期日之前照常使用，到期后才销毁" },
];

export type RenewalMutation = {
  mutateAsync: (vars: { mode: RenewMode; period?: number }) => Promise<unknown>;
  isPending: boolean;
};

export type TerminationMutation = {
  mutateAsync: (vars: { policy: TerminationPolicy }) => Promise<{ message?: string } | unknown>;
  isPending: boolean;
};

type RenewalInfo = ServiceInfo | (Omit<ServiceInfo, "possibleRenewPeriod"> & { possibleRenewPeriod?: number[] });

/** 续费策略对话框；到期终止使用 services/{id} 的 terminationPolicy，避免误调用立即终止接口。 */
export function RenewalDialog({
  serviceName,
  info,
  open,
  onOpenChange,
  mutation,
  termination,
}: {
  serviceName: string;
  info: RenewalInfo;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mutation?: RenewalMutation;
  termination?: TerminationMutation;
}) {
  const terminationOn = info.terminationScheduled ?? info.renewalDeleteAtExpiration;
  const currentMode: RenewMode = terminationOn ? "delete" : info.renewalType ? "auto" : "manual";
  const [mode, setMode] = useState<RenewMode>(currentMode);
  const [period, setPeriod] = useState(info.renewalPeriod || 1);
  const defaultUpdate = useUpdateRenewal(serviceName);
  const update = mutation ?? defaultUpdate;
  const defaultPolicy = useUpdateTerminationPolicy(serviceName);
  const policy = termination ?? defaultPolicy;

  useEffect(() => {
    if (open) {
      setMode(currentMode);
      setPeriod(info.renewalPeriod || 1);
    }
  }, [open, currentMode, info.renewalPeriod]);

  const periods = info.possibleRenewPeriod && info.possibleRenewPeriod.length > 0
    ? info.possibleRenewPeriod
    : [1, 3, 6, 12];
  const sameSelection =
    mode === currentMode &&
    period === info.renewalPeriod &&
    !(mode === "delete" && info.terminationAction === "terminateAtEngagementDate");

  const handleSubmit = async () => {
    try {
      if (mode === "delete") {
        const result = await policy.mutateAsync({ policy: "terminateAtExpirationDate" });
        const message = (result as { message?: string } | undefined)?.message;
        toast.success(message || "已设为到期终止", { duration: 6000 });
        onOpenChange(false);
        return;
      }

      if (terminationOn) {
        await policy.mutateAsync({ policy: "empty" });
      }
      await update.mutateAsync({ mode, period });
      toast.success(terminationOn ? "已取消终止并更新续费策略" : "续费策略已更新");
      onOpenChange(false);
    } catch (error: unknown) {
      toast.error(apiErrorText(error, "续费策略更新失败"), { duration: 8000 });
    }
  };

  const busy = update.isPending || policy.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Repeat className="h-5 w-5" />
            修改续费策略
          </DialogTitle>
          <DialogDescription>{serviceName}</DialogDescription>
        </DialogHeader>

        {info.terminationStateUnknown && (
          <div className="flex gap-2.5 rounded-xl border border-amber-500/40 bg-amber-500/10 p-3">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
            <p className="text-[12px] text-muted-foreground">
              OVH 生命周期状态读取失败，当前显示可能是旧的续费字段。保存前请确认服务状态，避免覆盖未知的终止计划。
            </p>
          </div>
        )}

        {info.renewalForced ? (
          <div className="flex gap-2.5 rounded-xl border border-amber-500/40 bg-amber-500/10 p-3">
            <Lock className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
            <div className="text-[12px]">
              <p className="mb-1 font-semibold text-amber-700 dark:text-amber-300">合同期内，无法修改</p>
              <p className="text-muted-foreground">该服务处于 OVH 合同期内，续费策略由 OVH 锁定。</p>
            </div>
          </div>
        ) : (
          <div className="space-y-3 py-1">
            <div className="space-y-1.5">
              {MODE_OPTIONS.map((option) => {
                const selected = mode === option.value;
                return (
                  <button
                    key={option.value}
                    type="button"
                    onClick={() => setMode(option.value)}
                    className={[
                      "w-full rounded-xl border px-3.5 py-2.5 text-left transition-colors",
                      selected ? "border-primary bg-primary/5" : "border-border bg-secondary/30 hover:bg-secondary/50",
                    ].join(" ")}
                  >
                    <div className="flex items-center gap-2">
                      <div className={["flex h-4 w-4 shrink-0 items-center justify-center rounded-full border-2", selected ? "border-primary" : "border-muted-foreground/40"].join(" ")}>
                        {selected && <div className="h-2 w-2 rounded-full bg-primary" />}
                      </div>
                      <span className="text-[13px] font-semibold">{option.label}</span>
                      {currentMode === option.value && <span className="ml-auto text-[10px] text-muted-foreground">当前</span>}
                    </div>
                    <p className="mt-1 ml-6 text-[11px] text-muted-foreground">{option.desc}</p>
                  </button>
                );
              })}
            </div>

            {mode !== "delete" && (
              <div className="pt-1">
                <label className="mb-1.5 block text-[12px] font-semibold">续费周期</label>
                <Select value={String(period)} onValueChange={(value) => setPeriod(Number(value))}>
                  <SelectTrigger className="h-9"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {periods.map((item) => <SelectItem key={item} value={String(item)}>{item} 个月</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
            )}

            {mode === "delete" && (
              <div className="flex gap-2 rounded-xl border border-destructive/40 bg-destructive/5 p-2.5">
                <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
                <p className="text-[11px] text-muted-foreground">
                  设为“到期终止”后，服务在到期日（{info.terminationDate || info.expiration ? new Date(info.terminationDate || info.expiration).toLocaleDateString("zh-CN") : "—"}）才会销毁，不会立即关机。
                </p>
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>取消</Button>
          {!info.renewalForced && (
            <Button onClick={handleSubmit} disabled={busy || sameSelection} variant={mode === "delete" ? "destructive" : "default"}>
              {busy ? "提交中…" : "保存"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
