import { useEffect, useState } from "react";
import {
  SERVER_CONTROL_ACCOUNT_KEY,
  getActiveServerControlAccount,
  setActiveServerControlAccount,
} from "@/lib/http";

const EVT = "ovh-active-account-changed";

/** 服务器控制 tab 活跃账户 ID。localStorage 持久化,跨组件同步。
 * 依赖账户的查询使用带账户 ID 的键，切换时会自动订阅新身份。
 */
export function useActiveServerControlAccount(): [string, (id: string) => void] {
  const [accountId, setAccountId] = useState<string>(() => getActiveServerControlAccount());

  useEffect(() => {
    // 监听跨组件 / 跨 tab 的活跃账户变化
    const onChange = (event: Event) => {
      if (event instanceof StorageEvent && event.key !== null && event.key !== SERVER_CONTROL_ACCOUNT_KEY) return;
      setAccountId(getActiveServerControlAccount());
    };
    window.addEventListener(EVT, onChange);
    window.addEventListener("storage", onChange);
    return () => {
      window.removeEventListener(EVT, onChange);
      window.removeEventListener("storage", onChange);
    };
  }, []);

  const set = (id: string) => {
    if (id === accountId) return;
    setActiveServerControlAccount(id);
    setAccountId(id);
  };
  return [accountId, set];
}
