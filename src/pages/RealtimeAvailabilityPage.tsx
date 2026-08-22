import { useEffect, useMemo, useState } from "react";
import { Helmet } from "react-helmet-async";
import {
  ChevronLeft,
  ChevronRight,
  Database,
  Download,
  Filter,
  RefreshCw,
  Search,
  TrendingUp,
} from "lucide-react";
import { toast } from "sonner";

import { AppLayout } from "@/components/layout/AppLayout";
import { PageHeader } from "@/components/common/PageHeader";
import { EmptyState } from "@/components/common/EmptyState";
import { Skeleton } from "@/components/common/Skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import {
  useRealtimeAvailability,
  type AvailabilityItem,
  type AvailabilityRegion,
} from "@/hooks/use-availability";

type AvailabilityFilter = "all" | "available" | "unavailable" | "1h";
type MemoryFilter = "all" | "lte128" | "128to256" | "256to512" | "gte1024";
type SortField = "planCode" | "memory" | "availability";
type SortOrder = "asc" | "desc";

const REGION_INFO: Record<AvailabilityRegion, { label: string; shortLabel: string; url: string }> = {
  eu: {
    label: "欧洲 (EU)",
    shortLabel: "EU",
    url: "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities",
  },
  ca: {
    label: "加拿大 (CA)",
    shortLabel: "CA",
    url: "https://ca.api.ovh.com/v1/dedicated/server/datacenter/availabilities",
  },
};

const PAGE_SIZE = 50;

function isAvailable(status: string | undefined): boolean {
  return !!status && status !== "unavailable" && status !== "unknown";
}

function memorySize(memory: string): number {
  const match = memory.match(/(\d+(?:\.\d+)?)\s*(?:g|gb)/i);
  return match ? Number(match[1]) : 0;
}

function statusInfo(status: string) {
  switch (status) {
    case "1H-low":
      return {
        text: "1小时-低库存",
        className: "border-warning/30 bg-warning/10 text-warning",
      };
    case "1H-high":
      return {
        text: "1小时-高库存",
        className: "border-primary/30 bg-primary/10 text-primary",
      };
    case "72H":
      return {
        text: "72小时",
        className: "border-accent/30 bg-accent/10 text-accent",
      };
    case "480H":
      return {
        text: "480小时",
        className: "border-purple-500/30 bg-purple-500/10 text-purple-300",
      };
    case "unavailable":
      return {
        text: "不可用",
        className: "border-destructive/30 bg-destructive/10 text-destructive",
      };
    case "unknown":
      return {
        text: "未知",
        className: "border-border bg-muted/50 text-muted-foreground",
      };
    default:
      return {
        text: status || "未知",
        className: "border-accent/30 bg-accent/10 text-accent",
      };
  }
}

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  return "获取 OVH 实时可用性失败";
}

function RealtimeAvailabilityPage() {
  const [region, setRegion] = useState<AvailabilityRegion>("eu");
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [datacenter, setDatacenter] = useState("all");
  const [availability, setAvailability] = useState<AvailabilityFilter>("all");
  const [memory, setMemory] = useState<MemoryFilter>("all");
  const [sortField, setSortField] = useState<SortField>("planCode");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");
  const [page, setPage] = useState(1);
  const query = useRealtimeAvailability(region);

  const items = useMemo(() => query.data?.items || [], [query.data?.items]);
  const regionInfo = REGION_INFO[region];

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedSearch(search.trim().toLowerCase()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);

  useEffect(() => {
    setPage(1);
  }, [region, debouncedSearch, datacenter, availability, memory, sortField, sortOrder]);

  const datacenters = useMemo(() => {
    const codes = new Set<string>();
    for (const item of items) {
      for (const dc of item.datacenters || []) {
        if (dc.datacenter) codes.add(dc.datacenter.toLowerCase());
      }
    }
    return Array.from(codes).sort((a, b) => a.localeCompare(b));
  }, [items]);

  useEffect(() => {
    if (datacenter !== "all" && !datacenters.includes(datacenter)) {
      setDatacenter("all");
    }
  }, [datacenter, datacenters]);

  const filteredItems = useMemo(() => {
    const filtered = items.filter((item) => {
      if (debouncedSearch) {
        const haystack = [item.planCode, item.server, item.fqn, item.memory, item.storage, item.systemStorage]
          .filter(Boolean)
          .join(" ")
          .toLowerCase();
        if (!haystack.includes(debouncedSearch)) return false;
      }

      const dcList = item.datacenters || [];
      if (datacenter !== "all" && !dcList.some((dc) => dc.datacenter.toLowerCase() === datacenter)) {
        return false;
      }
      if (availability === "available" && !dcList.some((dc) => isAvailable(dc.availability))) {
        return false;
      }
      if (availability === "unavailable" && !dcList.every((dc) => !isAvailable(dc.availability))) {
        return false;
      }
      if (availability === "1h" && !dcList.some((dc) => dc.availability === "1H-low" || dc.availability === "1H-high")) {
        return false;
      }

      if (memory !== "all") {
        const size = memorySize(item.memory || "");
        if (memory === "lte128" && !(size > 0 && size <= 128)) return false;
        if (memory === "128to256" && !(size > 128 && size <= 256)) return false;
        if (memory === "256to512" && !(size > 256 && size <= 512)) return false;
        if (memory === "gte1024" && size < 1024) return false;
      }
      return true;
    });

    filtered.sort((a, b) => {
      let comparison = 0;
      if (sortField === "planCode") comparison = a.planCode.localeCompare(b.planCode);
      if (sortField === "memory") comparison = memorySize(a.memory) - memorySize(b.memory);
      if (sortField === "availability") {
        const availableA = (a.datacenters || []).filter((dc) => isAvailable(dc.availability)).length;
        const availableB = (b.datacenters || []).filter((dc) => isAvailable(dc.availability)).length;
        comparison = availableA - availableB;
      }
      return sortOrder === "asc" ? comparison : -comparison;
    });
    return filtered;
  }, [items, debouncedSearch, datacenter, availability, memory, sortField, sortOrder]);

  const totalPages = Math.max(1, Math.ceil(filteredItems.length / PAGE_SIZE));
  const paginatedItems = filteredItems.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);

  useEffect(() => {
    setPage((current) => Math.min(current, totalPages));
  }, [totalPages]);

  const stats = useMemo(
    () => ({
      total: filteredItems.length,
      available: filteredItems.filter((item) => item.datacenters?.some((dc) => isAvailable(dc.availability))).length,
      oneHour: filteredItems.filter((item) =>
        item.datacenters?.some((dc) => dc.availability === "1H-low" || dc.availability === "1H-high")
      ).length,
    }),
    [filteredItems]
  );

  const selectRegion = (nextRegion: AvailabilityRegion) => {
    if (nextRegion === region) return;
    setRegion(nextRegion);
    setDatacenter("all");
  };

  const changeSort = (field: SortField, initialOrder: SortOrder = "asc") => {
    if (sortField === field) {
      setSortOrder((value) => (value === "asc" ? "desc" : "asc"));
      return;
    }
    setSortField(field);
    setSortOrder(initialOrder);
  };

  const exportData = () => {
    const blob = new Blob([JSON.stringify(filteredItems, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `ovh-availability-${region}-${new Date().toISOString().slice(0, 10)}.json`;
    link.click();
    URL.revokeObjectURL(url);
    toast.success("实时可用性数据已导出");
  };

  const refresh = async () => {
    const result = await query.refetch();
    if (result.error) {
      toast.error(errorMessage(result.error));
    } else {
      toast.success(`${regionInfo.label}数据已刷新`);
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Database}
        title="实时可用性"
        description="查询 OVH 专用服务器在各数据中心的实时库存，无需 OVH API 认证"
        action={
          <>
            <Button variant="outline" size="sm" onClick={exportData} disabled={filteredItems.length === 0}>
              <Download className="h-4 w-4" />
              导出 JSON
            </Button>
            <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={query.isFetching}>
              <RefreshCw className={cn("h-4 w-4", query.isFetching && "animate-spin")} />
              刷新
            </Button>
          </>
        }
      />

      <Card className="overflow-hidden">
        <CardContent className="p-4 sm:p-5">
          <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold">OVH 公开 API</span>
                <span className="rounded-full border border-primary/25 bg-primary/10 px-2 py-0.5 text-[11px] font-medium text-primary">
                  {regionInfo.label}
                </span>
              </div>
              <code className="mt-2 block break-all font-mono text-[11px] text-muted-foreground sm:text-xs">
                {query.data?.source || regionInfo.url}
              </code>
              <p className="mt-1 text-[11px] text-muted-foreground">
                {query.data?.fetchedAt
                  ? `数据时间：${new Date(query.data.fetchedAt).toLocaleString("zh-CN", { hour12: false })}`
                  : "切换区域后将立即查询对应接口"}
              </p>
            </div>
            <div className="inline-flex w-full rounded-lg border border-border bg-muted/30 p-1 sm:w-auto" aria-label="选择 OVH 区域">
              {(["eu", "ca"] as const).map((code) => (
                <Button
                  key={code}
                  type="button"
                  size="sm"
                  variant={region === code ? "default" : "ghost"}
                  className="flex-1 sm:min-w-32"
                  onClick={() => selectRegion(code)}
                >
                  {REGION_INFO[code].label}
                </Button>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>

      {query.isLoading ? (
        <LoadingState />
      ) : query.isError && items.length === 0 ? (
        <Card>
          <EmptyState
            icon={Database}
            title="实时可用性加载失败"
            description={errorMessage(query.error)}
            action={
              <Button variant="outline" onClick={() => void refresh()}>
                <RefreshCw className="h-4 w-4" />
                重新加载
              </Button>
            }
          />
        </Card>
      ) : (
        <>
          {query.isError && (
            <div className="rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning" role="alert">
              最新刷新失败：{errorMessage(query.error)}。当前继续显示上次成功获取的数据。
            </div>
          )}
          <div className="grid grid-cols-3 gap-2 sm:gap-4">
            <StatCard icon={Database} label="总记录数" mobileLabel="总数" value={stats.total} />
            <StatCard icon={TrendingUp} label="有货服务器" mobileLabel="有货" value={stats.available} valueClassName="text-primary" />
            <StatCard icon={Filter} label="1小时内" mobileLabel="1H内" value={stats.oneHour} valueClassName="text-warning" />
          </div>

          <Card>
            <CardContent className="p-4">
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
                <div className="relative">
                  <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    value={search}
                    onChange={(event) => setSearch(event.target.value)}
                    placeholder="搜索型号、服务器、内存、存储..."
                    className="pl-9"
                  />
                </div>
                <Select value={datacenter} onValueChange={setDatacenter}>
                  <SelectTrigger><SelectValue placeholder="所有数据中心" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">所有数据中心</SelectItem>
                    {datacenters.map((code) => (
                      <SelectItem key={code} value={code}>{code.toUpperCase()}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Select value={availability} onValueChange={(value) => setAvailability(value as AvailabilityFilter)}>
                  <SelectTrigger><SelectValue placeholder="所有状态" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">所有状态</SelectItem>
                    <SelectItem value="available">有货</SelectItem>
                    <SelectItem value="1h">1小时内</SelectItem>
                    <SelectItem value="unavailable">无货</SelectItem>
                  </SelectContent>
                </Select>
                <Select value={memory} onValueChange={(value) => setMemory(value as MemoryFilter)}>
                  <SelectTrigger><SelectValue placeholder="所有内存" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">所有内存</SelectItem>
                    <SelectItem value="lte128">≤ 128GB</SelectItem>
                    <SelectItem value="128to256">128GB - 256GB</SelectItem>
                    <SelectItem value="256to512">256GB - 512GB</SelectItem>
                    <SelectItem value="gte1024">≥ 1TB</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="mt-4 flex flex-wrap items-center gap-2">
                <span className="mr-1 text-xs text-muted-foreground">排序</span>
                <SortButton label="型号" active={sortField === "planCode"} order={sortOrder} onClick={() => changeSort("planCode")} />
                <SortButton label="内存" active={sortField === "memory"} order={sortOrder} onClick={() => changeSort("memory")} />
                <SortButton label="可用性" active={sortField === "availability"} order={sortOrder} onClick={() => changeSort("availability", "desc")} />
              </div>
            </CardContent>
          </Card>

          {filteredItems.length === 0 ? (
            <Card>
              <EmptyState
                icon={items.length === 0 ? Database : Filter}
                title={items.length === 0 ? `${regionInfo.label}暂无可用性数据` : "没有匹配的结果"}
                description={items.length === 0 ? "OVH 当前未返回记录，可稍后刷新重试" : "请修改搜索词或筛选条件"}
              />
            </Card>
          ) : (
            <>
              <div className="space-y-3">
                {paginatedItems.map((item, index) => (
                  <AvailabilityCard key={item.fqn || `${item.planCode}-${index}`} item={item} />
                ))}
              </div>
              {totalPages > 1 && (
                <Pagination
                  page={page}
                  totalPages={totalPages}
                  total={filteredItems.length}
                  onPageChange={setPage}
                />
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}

function StatCard({
  icon: Icon,
  label,
  mobileLabel,
  value,
  valueClassName,
}: {
  icon: typeof Database;
  label: string;
  mobileLabel: string;
  value: number;
  valueClassName?: string;
}) {
  return (
    <Card>
      <CardContent className="p-3 sm:p-5">
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground sm:text-xs">
          <Icon className="h-3.5 w-3.5" />
          <span className="hidden sm:inline">{label}</span>
          <span className="sm:hidden">{mobileLabel}</span>
        </div>
        <div className={cn("mt-1.5 text-xl font-semibold tabular-nums sm:text-2xl", valueClassName)}>{value}</div>
      </CardContent>
    </Card>
  );
}

function SortButton({
  label,
  active,
  order,
  onClick,
}: {
  label: string;
  active: boolean;
  order: SortOrder;
  onClick: () => void;
}) {
  return (
    <Button type="button" size="sm" variant={active ? "terminal" : "ghost"} onClick={onClick}>
      {label} {active && (order === "asc" ? "↑" : "↓")}
    </Button>
  );
}

function AvailabilityCard({ item }: { item: AvailabilityItem }) {
  return (
    <Card className="transition-colors hover:border-primary/30">
      <CardContent className="p-4 sm:p-5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="truncate font-mono text-sm font-semibold text-primary sm:text-base">{item.planCode}</h2>
            <p className="mt-0.5 truncate text-xs text-muted-foreground">{item.server}</p>
          </div>
          <code className="hidden max-w-[48%] truncate text-right font-mono text-[10px] text-muted-foreground md:block" title={item.fqn}>
            {item.fqn}
          </code>
        </div>
        <div className="mt-3 grid grid-cols-2 gap-2 border-b border-border/70 pb-3 text-xs sm:grid-cols-3">
          <Spec label="内存" value={item.memory} />
          <Spec label="存储" value={item.storage} />
          {item.systemStorage && <Spec label="系统盘" value={item.systemStorage} />}
        </div>
        <p className="mb-2 mt-3 text-[11px] font-medium text-muted-foreground">
          数据中心可用性（{item.datacenters?.length || 0} 个）
        </p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5 xl:grid-cols-6">
          {(item.datacenters || []).map((dc) => {
            const info = statusInfo(dc.availability);
            return (
              <div key={`${item.fqn}-${dc.datacenter}`} className={cn("rounded-lg border px-2.5 py-2", info.className)}>
                <div className="font-mono text-[11px] font-semibold text-foreground">{dc.datacenter.toUpperCase()}</div>
                <div className="mt-0.5 text-[10px] font-medium">{info.text}</div>
              </div>
            );
          })}
        </div>
      </CardContent>
    </Card>
  );
}

function Spec({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <span className="text-muted-foreground">{label}：</span>
      <span className="ml-1 break-words text-foreground">{value || "—"}</span>
    </div>
  );
}

function Pagination({
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
        <span className="text-xs text-muted-foreground">显示 {start} - {end} / 共 {total} 条</span>
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

function LoadingState() {
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-3 gap-2 sm:gap-4">
        {[0, 1, 2].map((value) => <Skeleton key={value} className="h-24 rounded-2xl" />)}
      </div>
      <Skeleton className="h-28 rounded-2xl" />
      {[0, 1, 2].map((value) => <Skeleton key={value} className="h-48 rounded-2xl" />)}
    </div>
  );
}

const Page = () => (
  <>
    <Helmet><title>实时可用性 | OVH WebUI</title></Helmet>
    <AppLayout><RealtimeAvailabilityPage /></AppLayout>
  </>
);

export default Page;
