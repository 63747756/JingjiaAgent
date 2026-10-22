import { AlertCircle, Loader2, RefreshCw, FolderOpen } from "lucide-react";
import { Button } from "@/components/ui/button";

export function WorkLoading({ text = "正在加载…" }: { text?: string }) {
  return (
    <div
      role="status"
      className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground"
    >
      <Loader2 className="size-4 animate-spin" />
      {text}
    </div>
  );
}
export function WorkError({
  message,
  retry,
}: {
  message: string;
  retry?: () => void;
}) {
  return (
    <div
      role="alert"
      className="rounded-xl border border-destructive/20 bg-destructive/5 p-4 text-sm"
    >
      <div className="flex items-center gap-2">
        <AlertCircle className="size-4 text-destructive" />
        <span>{message}</span>
      </div>
      {retry && (
        <Button variant="outline" size="sm" className="mt-3" onClick={retry}>
          <RefreshCw className="size-3.5" />
          重新加载
        </Button>
      )}
    </div>
  );
}
export function WorkEmpty({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center rounded-2xl border border-dashed px-6 py-12 text-center">
      <FolderOpen className="mb-3 size-7 text-muted-foreground/50" />
      <h3 className="text-sm font-medium">{title}</h3>
      {description && (
        <p className="mt-2 max-w-md text-sm leading-6 text-muted-foreground">
          {description}
        </p>
      )}
      {children && <div className="mt-4">{children}</div>}
    </div>
  );
}
