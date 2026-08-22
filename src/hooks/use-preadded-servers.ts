import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/http";
import { qk } from "@/lib/query";
import type { AvailabilityItem } from "@/hooks/ovh/use-availability";

export type PreaddedServer = AvailabilityItem & {
  region: "eu" | "ca";
  comparisonRegion: "ovh-ie" | "ovh-ca";
  detectedAt: string;
};

export function usePreaddedServers(region: "all" | "eu" | "ca" = "all") {
  return useQuery({
    queryKey: qk.preaddedServers.list(region),
    queryFn: async () => {
      const res = await api.get<{ items?: PreaddedServer[] }>("/preadded-servers", {
        params: { region },
      });
      return (res.data.items || []) as PreaddedServer[];
    },
    staleTime: 60_000,
    refetchInterval: 60_000,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}
