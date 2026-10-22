import { useImperativeHandle, useLayoutEffect, useRef, type ComponentProps } from "react";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

export function MessageComposerFrame({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("min-w-0 rounded-2xl border bg-card p-3 shadow-sm transition-shadow focus-within:border-primary/40 focus-within:shadow-md sm:p-3.5", className)} {...props} />;
}

export function MessageComposerTextarea({ ref, value, compact = false, className, ...props }: ComponentProps<"textarea"> & { compact?: boolean }) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => textareaRef.current!, []);

  useLayoutEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const resize = () => {
      textarea.style.height = "0px";
      const maxHeight = Math.max(64, Math.min(compact ? 120 : 144, window.innerHeight * 0.22));
      const height = Math.max(64, Math.min(textarea.scrollHeight, maxHeight));
      textarea.style.height = `${height}px`;
      textarea.style.overflowY = textarea.scrollHeight > height ? "auto" : "hidden";
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(textarea.parentElement!);
    window.addEventListener("resize", resize);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", resize);
    };
  }, [value, compact]);

  return <Textarea ref={textareaRef} value={value} className={cn("h-16 min-h-0 min-w-0 w-full resize-none overflow-y-auto overscroll-contain border-0 bg-transparent p-0.5 text-base leading-6 shadow-none focus-visible:ring-0 [field-sizing:fixed]", className)} {...props} />;
}
