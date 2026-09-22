import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, apiErrorText } from "@/lib/http";
import { qk } from "@/lib/query";
import { toast } from "sonner";

export interface PurchaseHistory {
  id: string;
  accountId: string;
  /** 关联的抢购队列任务 ID（后端 PurchaseHistoryEntry.taskId） */
  taskId?: string;
  planCode: string;
  datacenter: string;
  options?: string[];
  status: "success" | "failed" | "uncertain";
  orderId?: string;
  orderUrl?: string;
  errorMessage?: string;
  purchaseTime: string;
  /** 抢购到这单时一共尝试了几次（后端 attemptCount） */
  attemptCount?: number;
  expirationTime?: string;
  price?: {
    withTax?: number;
    withoutTax?: number;
    tax?: number;
    currencyCode?: string;
  };
  /** OVH /me/order/{id}/status 的最近状态快照 */
  orderStatus?: string;
  orderStatusAt?: string;
}

/** 抢购历史 */
export function useHistory() {
  return useQuery({
    queryKey: qk.history(),
    queryFn: async () => (await api.get<PurchaseHistory[]>("/purchase-history")).data,
  });
}

/** 清空抢购历史 */
export function useClearHistory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.delete("/purchase-history")).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.history() });
      toast.success("已清空购买历史");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "清空失败")),
  });
}
