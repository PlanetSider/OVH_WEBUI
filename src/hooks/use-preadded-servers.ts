import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/http";
import { qk } from "@/lib/query";

export type PreaddedServerDatacenter = {
  datacenter: string;
  availability: string;
  availableVariants: number;
  reportedVariants: number;
};

export type PreaddedServer = {
  planCode: string;
  server: string;
  regions: Array<"eu" | "ca">;
  variantCount: number;
  memories: string[];
  storages: string[];
  systemStorages: string[];
  datacenters: PreaddedServerDatacenter[];
};

export type PreaddedServersResponse = {
  region: "all" | "eu" | "ca";
  items: PreaddedServer[];
  total: number;
  page: number;
  pageSize: number;
  totalPages: number;
  lastComparedAt: string;
  comparisonTimes: Partial<Record<"eu" | "ca", string>>;
};

export function usePreaddedServers(
  region: "all" | "eu" | "ca" = "all",
  page = 1,
  pageSize = 20,
  search = "",
) {
  return useQuery({
    queryKey: qk.preaddedServers.list(region, page, pageSize, search),
    queryFn: async () => {
      const res = await api.get<PreaddedServersResponse>("/preadded-servers", {
        params: { region, page, pageSize, search: search || undefined },
      });
      return res.data;
    },
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}
