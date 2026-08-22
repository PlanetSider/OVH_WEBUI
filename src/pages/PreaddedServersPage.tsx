import { useMemo, useState } from "react";
import { Helmet } from "react-helmet-async";
import { Database, RefreshCw, Search, Sparkles } from "lucide-react";

import { AppLayout } from "@/components/layout/AppLayout";
import { PageHeader } from "@/components/common/PageHeader";
import { EmptyState } from "@/components/common/EmptyState";
import { Skeleton } from "@/components/common/Skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { usePreaddedServers } from "@/hooks/use-preadded-servers";

type Region = "all" | "eu" | "ca";

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
  const query = usePreaddedServers(region);
  const items = useMemo(() => query.data || [], [query.data]);
  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    if (!term) return items;
    return items.filter((item) =>
      [item.planCode, item.server, item.fqn, item.memory, item.storage, item.region, item.comparisonRegion]
        .filter(Boolean)
        .join(" ")
        .toLowerCase()
        .includes(term)
    );
  }, [items, search]);

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Sparkles}
        title="预增服务器"
        description="实时可用性快照中已出现、但尚未出现在对应区域服务器列表中的服务器"
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
              onChange={(event) => setSearch(event.target.value)}
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
                onClick={() => setRegion(code)}
              >
                {code === "all" ? "全部" : code.toUpperCase()}
              </Button>
            ))}
          </div>
        </CardContent>
      </Card>

      <div className="grid gap-3 sm:grid-cols-2">
        <ComparisonRule source="EU 实时可用性" target="ovh-ie" />
        <ComparisonRule source="CA 实时可用性" target="ovh-ca" />
      </div>

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
            icon={items.length === 0 ? Database : Search}
            title={items.length === 0 ? "暂无预增服务器" : "没有匹配的结果"}
            description={items.length === 0 ? "后台整点刷新实时可用性并完成比对后，新增条目会显示在这里" : "请修改搜索条件"}
          />
        </Card>
      ) : (
        <div className="space-y-3">
          {filtered.map((item, index) => (
            <Card key={item.fqn || `${item.planCode}-${index}`} className="transition-colors hover:border-primary/30">
              <CardContent className="p-4 sm:p-5">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="truncate font-mono text-sm font-semibold text-primary sm:text-base">{item.planCode}</h2>
                      <span className="rounded-full border border-accent/25 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent">
                        {item.region.toUpperCase()}
                      </span>
                      <span className="rounded-full border border-border bg-muted/50 px-2 py-0.5 text-[10px] font-medium text-muted-foreground">
                        对比 {item.comparisonRegion}
                      </span>
                    </div>
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">{item.server}</p>
                  </div>
                  <div className="text-right text-[10px] text-muted-foreground">
                    <div>发现时间</div>
                    <div>{new Date(item.detectedAt).toLocaleString("zh-CN", { hour12: false })}</div>
                  </div>
                </div>
                <code className="mt-3 block truncate font-mono text-[10px] text-muted-foreground" title={item.fqn}>{item.fqn}</code>
                <div className="mt-3 grid grid-cols-2 gap-2 border-y border-border/70 py-3 text-xs sm:grid-cols-3">
                  <Spec label="内存" value={item.memory} />
                  <Spec label="存储" value={item.storage} />
                  {item.systemStorage && <Spec label="系统盘" value={item.systemStorage} />}
                </div>
                <p className="mb-2 mt-3 text-[11px] font-medium text-muted-foreground">数据中心可用性</p>
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5 xl:grid-cols-6">
                  {(item.datacenters || []).map((dc) => (
                    <div key={`${item.fqn}-${dc.datacenter}`} className={cn("rounded-lg border px-2.5 py-2", statusInfo(dc.availability))}>
                      <div className="font-mono text-[11px] font-semibold text-foreground">{dc.datacenter.toUpperCase()}</div>
                      <div className="mt-0.5 text-[10px] font-medium">{dc.availability || "未知"}</div>
                    </div>
                  ))}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}

function ComparisonRule({ source, target }: { source: string; target: "ovh-ie" | "ovh-ca" }) {
  return (
    <Card className="border-dashed">
      <CardContent className="flex items-center justify-between gap-3 p-3 text-xs">
        <span className="text-muted-foreground">{source}</span>
        <span className="font-medium text-foreground">仅对比 {target}</span>
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
