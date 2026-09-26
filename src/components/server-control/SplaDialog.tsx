import { useState } from "react";
import { AlertCircle, Zap } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { apiErrorText } from "@/lib/http";
import { useScopedAccountApi } from "@/hooks/ovh/use-account-scope";
import { toast } from "sonner";

const SPLA_TYPES = [
  { value: "os", label: "操作系统（Windows Server）" },
  { value: "sqlstd", label: "SQL Server 标准版" },
  { value: "sqlweb", label: "SQL Server 网页版" },
] as const;

export function SplaDialog({
  serviceName,
  open,
  onOpenChange,
}: {
  serviceName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const api = useScopedAccountApi();
  const [type, setType] = useState<(typeof SPLA_TYPES)[number]["value"]>("os");
  const [serialNumber, setSerialNumber] = useState("");
  const [pending, setPending] = useState(false);

  const submit = async () => {
    const serial = serialNumber.trim();
    if (!serial) {
      toast.error("请填写你购买的 SPLA 许可证序列号");
      return;
    }

    setPending(true);
    try {
      await api.post(`/server-control/${serviceName}/spla`, {
        type,
        serialNumber: serial,
      });
      toast.success("SPLA 许可证已提交登记");
      setSerialNumber("");
      onOpenChange(false);
    } catch (error: unknown) {
      toast.error(apiErrorText(error, "许可证提交失败"), { duration: 8000 });
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Zap className="h-5 w-5 text-amber-500" />
            登记 SPLA 许可证
          </DialogTitle>
          <DialogDescription className="font-mono text-xs">{serviceName}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3 py-2">
          <div>
            <label className="mb-1.5 block text-xs font-semibold">授权类型</label>
            <Select value={type} onValueChange={(value) => setType(value as typeof type)}>
              <SelectTrigger className="h-9">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SPLA_TYPES.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div>
            <label className="mb-1.5 block text-xs font-semibold">许可证序列号</label>
            <Input
              value={serialNumber}
              onChange={(event) => setSerialNumber(event.target.value)}
              placeholder="XXXXX-XXXXX-XXXXX-XXXXX-XXXXX"
              autoFocus
              disabled={pending}
            />
          </div>

          <div className="flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 p-2.5">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              请输入你自己购买的合法授权序列号。公开 KMS 密钥或无效序列号会被 OVH 拒绝。
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            取消
          </Button>
          <Button onClick={submit} disabled={pending || !serialNumber.trim()}>
            {pending ? "提交中…" : "确认登记"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
