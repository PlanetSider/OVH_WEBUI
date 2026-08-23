import { useMemo, useState } from "react";
import { Helmet } from "react-helmet-async";
import { ChevronLeft, ChevronRight, Database, RefreshCw, Search, Sparkles } from "lucide-react";

import { AppLayout } from "@/components/layout/AppLayout";
import { PageHeader } from "@/components/common/PageHeader";
import { EmptyState } from "@/components/common/EmptyState";
import { Skeleton } from "@/components/common/Skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { usePreaddedServers, type PreaddedServer } from "@/hooks/use-preadded-servers";

type Region = "all" | "eu" | "ca";

type AggregatedDatacenter = {
  datacenter: string;
  availability: string;
  availableVariants: number;
  reportedVariants: number;
};

type AggregatedServer = {
  planCode: string;
  server: string;
  regions: Array<"eu" | "ca">;
  detectedAt: string;
  variants: PreaddedServer[];
  memories: string[];
  storages: string[];
  systemStorages: string[];
  datacenters: AggregatedDatacenter[];
};

const PAGE_SIZE = 20;

function isAvailable(status: string) {
  return !!status && status !== "unavailable" && status !== "unknown";
}

function uniqueValues(values: Array<string | undefined>) {
  return Array.from(new Set(values.map((value) => value?.trim()).filter((value): value is string => !!value)));
}

function aggregateServers(items: PreaddedServer[]): AggregatedServer[] {
  const groups = new Map<string, PreaddedServer[]>();
  for (const item of items) {
    const key = item.planCode?.trim().toLowerCase();
    if (!key) continue;
    const variants = groups.get(key);
    if (variants) variants.push(item);
    else groups.set(key, [item]);
  }

  return Array.from(groups.values()).map((variants) => {
    const datacenterGroups = new Map<string, { statuses: string[]; availableVariants: number }>();
    for (const variant of variants) {
      for (const dc of variant.datacenters || []) {
        const code = dc.datacenter?.trim().toLowerCase();
        if (!code) continue;
        const group = datacenterGroups.get(code) || { statuses: [], availableVariants: 0 };
        group.statuses.push(dc.availability || "unknown");
        if (isAvailable(dc.availability)) group.availableVariants += 1;
        datacenterGroups.set(code, group);
      }
    }

    const datacenters = Array.from(datacenterGroups.entries())
      .map(([datacenter, group]) => ({
        datacenter,
        availability: group.statuses.find(isAvailable) || (group.statuses.includes("unavailable") ? "unavailable" : "unknown"),
        availableVariants: group.availableVariants,
        reportedVariants: group.statuses.length,
      }))
      .sort((a, b) => a.datacenter.localeCompare(b.datacenter));

    return {
      planCode: variants[0].planCode.trim(),
      server: variants.find((item) => item.server)?.server || variants[0].planCode,
      regions: Array.from(new Set(variants.map((item) => item.region))).sort(),
      detectedAt: variants.reduce(
        (latest, item) => (Date.parse(item.detectedAt) > Date.parse(latest) ? item.detectedAt : latest),
        variants[0].detectedAt,
      ),
      variants,
      memories: uniqueValues(variants.map((item) => item.memory)),
      storages: uniqueValues(variants.map((item) => item.storage)),
      systemStorages: uniqueValues(variants.map((item) => item.systemStorage)),
      datacenters,
    };
  });
}

function statusInfo(status: string) {
  if (status === "1H-low") return "border-warning/30 bg-warning/10 text-warning";
  if (status === "1H-high") return "border-primary/30 bg-primary/10 text-primary";
  if (status === "unavailable") return "border-destructive/30 bg-destructive/10 text-destructive";
  if (status === "unknown") return "border-border bg-muted/50 text-muted-foreground";
  return "border-accent/30 bg-accent/10 text-accent";
}

function PreaddedServersPage() {
  const [region, setRegion] = useState<Region>("all");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const query = usePreaddedServers(region);
  const items = useMemo(() => query.data || [], [query.data]);
  const grouped = useMemo(() => aggregateServers(items), [items]);
  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    if (!term) return grouped;
    return grouped.filter((item) => {
      const searchable = [
        item.planCode,
        item.server,
        ...item.regions,
        ...item.memories,
        ...item.storages,
        ...item.systemStorages,
        ...item.variants.map((variant) => variant.fqn),
      ];
      return searchable.filter(Boolean).join(" ").toLowerCase().includes(term);
    });
  }, [grouped, search]);
  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const currentPage = Math.min(page, totalPages);
  const paginated = filtered.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Sparkles}
        title="预增服务器"
        description="按型号聚合显示实时可用性中新出现的服务器"
        action={
          <Button variant="outline" size="sm" onClick={() => void query.refetch()} disabled={query.isFetching}>
            <RefreshCw className={cn("h-4 w-4", query.isFetching && "animate-spin")} />
            重新读取
          </Button>
        }
      />

      <Card>
        <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="relative min-w-0 flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setPage(1);
              }}
              placeholder="搜索型号、服务器、FQN..."
              className="pl-9"
            />
          </div>
          <div className="inline-flex w-full rounded-lg border border-border bg-muted/30 p-1 sm:w-auto">
            {(["all", "eu", "ca"] as const).map((code) => (
              <Button
                key={code}
                type="button"
                size="sm"
                variant={region === code ? "default" : "ghost"}
                className="flex-1 sm:min-w-24"
                onClick={() => {
                  setRegion(code);
                  setPage(1);
                }}
              >
                {code === "all" ? "全部" : code.toUpperCase()}
              </Button>
            ))}
          </div>
        </CardContent>
      </Card>

      {query.isLoading ? (
        <div className="space-y-3">
          {[0, 1, 2].map((value) => <Skeleton key={value} className="h-48 rounded-2xl" />)}
        </div>
      ) : query.isError ? (
        <Card>
          <EmptyState
            icon={Sparkles}
            title="预增服务器加载失败"
            description={query.error instanceof Error ? query.error.message : "读取预增服务器失败"}
            action={<Button variant="outline" onClick={() => void query.refetch()}><RefreshCw className="h-4 w-4" />重新加载</Button>}
          />
        </Card>
      ) : filtered.length === 0 ? (
        <Card>
          <EmptyState
            icon={grouped.length === 0 ? Database : Search}
            title={grouped.length === 0 ? "暂无预增服务器" : "没有匹配的结果"}
            description={grouped.length === 0 ? "后台整点刷新实时可用性并完成比对后，新增型号会显示在这里" : "请修改搜索条件"}
          />
        </Card>
      ) : (
        <>
          <div className="space-y-3">
          {paginated.map((item) => (
            <Card key={item.planCode.toLowerCase()} className="transition-colors hover:border-primary/30">
              <CardContent className="p-4 sm:p-5">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="truncate font-mono text-sm font-semibold text-primary sm:text-base">{item.planCode}</h2>
                      {item.regions.map((itemRegion) => (
                        <span key={itemRegion} className="rounded-full border border-accent/25 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent">
                          {itemRegion.toUpperCase()}
                        </span>
                      ))}
                      <span className="rounded-full border border-border bg-muted/50 px-2 py-0.5 text-[10px] font-medium text-muted-foreground">
                        {item.variants.length} 个配置
                      </span>
                    </div>
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">{item.server}</p>
                  </div>
                  <div className="text-right text-[10px] text-muted-foreground">
                    <div>发现时间</div>
                    <div>{new Date(item.detectedAt).toLocaleString("zh-CN", { hour12: false })}</div>
                  </div>
                </div>
                <div className="mt-3 grid grid-cols-1 gap-2 border-y border-border/70 py-3 text-xs sm:grid-cols-2 lg:grid-cols-3">
                  <Spec label="内存选项" value={item.memories.join("、")} />
                  <Spec label="存储选项" value={item.storages.join("、")} />
                  {item.systemStorages.length > 0 && <Spec label="系统盘选项" value={item.systemStorages.join("、")} />}
                </div>
                <p className="mb-2 mt-3 text-[11px] font-medium text-muted-foreground">数据中心可用配置</p>
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5 xl:grid-cols-6">
                  {item.datacenters.map((dc) => (
                    <div key={dc.datacenter} className={cn("rounded-lg border px-2.5 py-2", statusInfo(dc.availability))}>
                      <div className="font-mono text-[11px] font-semibold text-foreground">{dc.datacenter.toUpperCase()}</div>
                      <div className="mt-0.5 text-[10px] font-medium">
                        {dc.availableVariants}/{dc.reportedVariants} 个配置可用
                      </div>
                    </div>
                  ))}
                </div>
              </CardContent>
            </Card>
          ))}
          </div>
          {totalPages > 1 && (
            <ServerPagination
              page={currentPage}
              totalPages={totalPages}
              total={filtered.length}
              onPageChange={setPage}
            />
          )}
        </>
      )}
    </div>
  );
}

function ServerPagination({
  page,
  totalPages,
  total,
  onPageChange,
}: {
  page: number;
  totalPages: number;
  total: number;
  onPageChange: (page: number) => void;
}) {
  const start = (page - 1) * PAGE_SIZE + 1;
  const end = Math.min(page * PAGE_SIZE, total);
  return (
    <Card>
      <CardContent className="flex flex-col items-center justify-between gap-3 p-4 sm:flex-row">
        <span className="text-xs text-muted-foreground">显示 {start} - {end} / 共 {total} 个型号</span>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={() => onPageChange(1)} disabled={page === 1} className="hidden sm:inline-flex">首页</Button>
          <Button size="sm" variant="outline" onClick={() => onPageChange(Math.max(1, page - 1))} disabled={page === 1} aria-label="上一页">
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <span className="min-w-16 text-center font-mono text-xs text-muted-foreground"><strong className="text-primary">{page}</strong> / {totalPages}</span>
          <Button size="sm" variant="outline" onClick={() => onPageChange(Math.min(totalPages, page + 1))} disabled={page === totalPages} aria-label="下一页">
            <ChevronRight className="h-4 w-4" />
          </Button>
          <Button size="sm" variant="ghost" onClick={() => onPageChange(totalPages)} disabled={page === totalPages} className="hidden sm:inline-flex">末页</Button>
        </div>
      </CardContent>
    </Card>
  );
}

function Spec({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0"><span className="text-muted-foreground">{label}：</span><span className="ml-1 break-words text-foreground">{value || "—"}</span></div>;
}

const Page = () => (
  <>
    <Helmet><title>预增服务器 | OVH WebUI</title></Helmet>
    <AppLayout><PreaddedServersPage /></AppLayout>
  </>
);

export default Page;
