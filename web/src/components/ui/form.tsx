import {
  createContext,
  useContext,
  useEffect,
  useId,
  useState,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import type { FieldError } from "@/lib/problem";
import { cn } from "@/lib/utils";

/** Server-side field errors (`Problem.errors`), looked up by `Field path`. */
const ErrorsContext = createContext<FieldError[]>([]);
export const FieldErrors = ErrorsContext.Provider;

/** Messages for a field path, e.g. `interfaces[0].dhcp.pool.start`. */
export function useFieldErrors(path?: string): string[] {
  const all = useContext(ErrorsContext);
  if (!path) return [];
  return all.filter((e) => e.field === path).map((e) => e.message);
}

const control = cn(
  "h-9 w-full rounded-md border border-input bg-card px-3 text-sm",
  "placeholder:text-muted-foreground/70 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-ring",
  "disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-destructive",
);

type FieldProps = {
  label: string;
  /** API field path; server errors for it show under the control. */
  path?: string;
  hint?: ReactNode;
  className?: string;
  children: (props: { id: string; "aria-describedby"?: string; "aria-invalid"?: true }) => ReactNode;
};

/** Label, control, hint and errors, wired together for assistive technology. */
export function Field({ label, path, hint, className, children }: FieldProps) {
  const id = useId();
  const errors = useFieldErrors(path);
  const describedBy = [hint ? `${id}-hint` : "", errors.length ? `${id}-error` : ""].filter(Boolean).join(" ");
  return (
    <div className={cn("min-w-0", className)}>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium">
        {label}
      </label>
      {children({
        id,
        "aria-describedby": describedBy || undefined,
        "aria-invalid": errors.length ? true : undefined,
      })}
      {errors.length > 0 && (
        <p id={`${id}-error`} className="mt-1.5 text-sm text-destructive">
          {errors.join(" ")}
        </p>
      )}
      {hint && (
        <p id={`${id}-hint`} className="mt-1.5 text-sm text-muted-foreground">
          {hint}
        </p>
      )}
    </div>
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cn(control, className)} {...props} />;
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className={cn(control, "h-auto min-h-20 py-2", className)} {...props} />;
}

export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className={cn(control, "pr-8", className)} {...props} />;
}

type NumberInputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type"> & {
  value: number | undefined;
  onChange: (v: number | undefined) => void;
};

/**
 * A number field that lets the user clear and retype: it keeps the text and
 * reports a number only when the text is one (undefined when empty).
 */
export function NumberInput({ value, onChange, className, ...props }: NumberInputProps) {
  const [text, setText] = useState(value === undefined ? "" : String(value));
  useEffect(() => {
    // Follow outside changes (reload, discard) without fighting the user's typing.
    setText((cur) => (cur !== "" && Number(cur) === value ? cur : value === undefined ? "" : String(value)));
  }, [value]);
  return (
    <input
      type="number"
      inputMode="numeric"
      className={cn(control, "tabular-nums", className)}
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        const n = e.target.valueAsNumber;
        onChange(Number.isFinite(n) ? n : undefined);
      }}
      {...props}
    />
  );
}

type CheckProps = {
  label: ReactNode;
  hint?: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  className?: string;
};

export function Checkbox({ label, hint, checked, onChange, disabled, className }: CheckProps) {
  const id = useId();
  return (
    <div className={cn("flex gap-2.5", className)}>
      <input
        id={id}
        type="checkbox"
        className="mt-0.5 size-4 shrink-0 accent-primary"
        checked={checked}
        disabled={disabled}
        aria-describedby={hint ? `${id}-hint` : undefined}
        onChange={(e) => onChange(e.target.checked)}
      />
      <div className="min-w-0 text-sm">
        <label htmlFor={id} className={cn("font-medium", disabled && "opacity-60")}>
          {label}
        </label>
        {hint && (
          <p id={`${id}-hint`} className="mt-0.5 text-muted-foreground">
            {hint}
          </p>
        )}
      </div>
    </div>
  );
}

type RadioCardsProps<T extends string> = {
  legend: string;
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: string; description: string }[];
};

/** A small set of exclusive choices, each with a sentence saying what it does. */
export function RadioCards<T extends string>({ legend, value, onChange, options }: RadioCardsProps<T>) {
  const name = useId();
  return (
    <fieldset>
      <legend className="mb-1.5 text-sm font-medium">{legend}</legend>
      <div className="grid gap-2 sm:grid-cols-3">
        {options.map((o) => (
          <label
            key={o.value}
            className={cn(
              "flex cursor-pointer gap-2.5 rounded-md border bg-card p-3 text-sm",
              "has-focus-visible:outline-2 has-focus-visible:outline-ring",
              value === o.value && "border-primary bg-accent/50",
            )}
          >
            <input
              type="radio"
              name={name}
              className="mt-0.5 size-4 shrink-0 accent-primary"
              checked={value === o.value}
              onChange={() => onChange(o.value)}
            />
            <span>
              <span className="font-medium">{o.label}</span>
              <span className="mt-0.5 block text-muted-foreground">{o.description}</span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
