import type { ImgHTMLAttributes } from "react";
import { BRAND } from "@/lib/brand";
import { cn } from "@/lib/utils";

type Props = Omit<ImgHTMLAttributes<HTMLImageElement>, "src"> & {
  variant?: "auto" | "blue" | "white";
};

export function BrandLogo({ variant = "auto", className, alt = BRAND.chineseName, ...props }: Props) {
  if (variant !== "auto") {
    return <img {...props} src={variant === "white" ? BRAND.logoWhite : BRAND.logoBlue} alt={alt} className={className} />;
  }
  return <span className={cn("inline-flex shrink-0", className)}>
    <img {...props} src={BRAND.logoBlue} alt={alt} className="size-full dark:hidden" />
    <img {...props} src={BRAND.logoWhite} alt="" aria-hidden="true" className="hidden size-full dark:block" />
  </span>;
}
