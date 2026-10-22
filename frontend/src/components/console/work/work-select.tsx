import { useState, type ComponentType } from "react";
import { Check, ChevronDown, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { cn } from "@/lib/utils";

export type WorkSelectOption = {
  id: string;
  label: string;
  description?: string;
  icon?: ComponentType<{ className?: string }>;
};

export function WorkSelect({
  label,
  placeholder,
  searchPlaceholder,
  emptyText,
  value,
  options,
  icon: Icon,
  disabled,
  onChange,
  createLabel,
  onCreate,
  customLabel,
  onLoadMore,
  loadMoreLabel,
}: {
  label: string;
  placeholder: string;
  searchPlaceholder: string;
  emptyText: string;
  value: string;
  options: WorkSelectOption[];
  icon: ComponentType<{ className?: string }>;
  disabled?: boolean;
  onChange: (id: string) => void;
  createLabel?: string;
  onCreate?: () => void;
  customLabel?: (value: string) => string;
  onLoadMore?: () => void;
  loadMoreLabel?: string;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const selected = options.find((option) => option.id === value);
  const SelectedIcon = selected?.icon || Icon;
  return (
    <Popover open={open} onOpenChange={(value) => { setOpen(value); if (value) setSearch(""); }}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          role="combobox"
          aria-label={label}
          aria-expanded={open}
          disabled={disabled}
          className="h-8 max-w-52 gap-1.5 rounded-lg px-2 font-normal"
        >
          <SelectedIcon className="size-3.5 shrink-0" />
          <span className={cn("truncate", !value && "text-muted-foreground")}>
            {selected?.label || placeholder}
          </span>
          <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-80 max-w-[calc(100vw-2rem)] p-0"
      >
        <Command>
          <CommandInput placeholder={searchPlaceholder} value={search} onValueChange={setSearch} />
          <CommandList className="max-h-64 p-1">
            <CommandEmpty>{emptyText}</CommandEmpty>
            {options.map((option) => {
              const OptionIcon = option.icon || Icon;
              return (
                <CommandItem
                  key={option.id || "unlinked"}
                  value={`${option.id} ${option.label} ${option.description || ""}`}
                  onSelect={() => {
                    onChange(option.id);
                    setOpen(false);
                  }}
                  className="cursor-pointer gap-2 rounded-lg py-2"
                >
                  <OptionIcon className="size-4 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm">{option.label}</div>
                    {option.description && (
                      <div className="truncate text-xs text-muted-foreground">
                        {option.description}
                      </div>
                    )}
                  </div>
                  {option.id === value && (
                    <Check className="size-3.5 text-primary" />
                  )}
                </CommandItem>
              );
            })}
            {customLabel && search.trim() && !options.some(option => option.id === search.trim()) && (
              <CommandItem value={search} onSelect={() => { onChange(search.trim()); setOpen(false); setSearch(""); }}>
                {customLabel(search.trim())}
              </CommandItem>
            )}
          </CommandList>
        </Command>
        {onLoadMore && <div className="border-t p-1"><Button type="button" variant="ghost" size="sm" className="w-full font-normal" onClick={onLoadMore}>{loadMoreLabel}</Button></div>}
        {onCreate && (
          <div className="border-t p-1">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="w-full justify-start font-normal"
              onClick={() => {
                setOpen(false);
                onCreate();
              }}
            >
              <Plus className="size-4" />
              {createLabel}
            </Button>
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}
