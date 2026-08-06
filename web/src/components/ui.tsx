import {
  useEffect,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react';
import { IconCheck, IconX } from './icons';

export function Spinner({ className = 'h-4 w-4' }: { className?: string }) {
  return (
    <div
      className={`animate-spin rounded-full border-2 border-border-strong border-t-text ${className}`}
    />
  );
}

type ButtonVariant = 'primary' | 'outline' | 'ghost' | 'danger';

const buttonStyles: Record<ButtonVariant, string> = {
  primary:
    'bg-primary text-primary-text hover:opacity-90 disabled:bg-border disabled:text-faint',
  outline:
    'border border-border-strong text-text hover:bg-border disabled:opacity-50',
  ghost: 'text-text hover:bg-border disabled:opacity-50',
  danger: 'bg-red-600/90 text-white hover:bg-red-600 disabled:opacity-50',
};

export function Button({
  variant = 'primary',
  className = '',
  type = 'button',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      type={type}
      {...props}
      className={`inline-flex min-h-[36px] items-center justify-center gap-2 rounded-lg px-3.5 text-sm font-medium transition-colors disabled:cursor-not-allowed ${buttonStyles[variant]} ${className}`}
    />
  );
}

export function IconButton({
  className = '',
  type = 'button',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type={type}
      {...props}
      className={`inline-flex h-11 w-11 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text disabled:cursor-not-allowed disabled:opacity-40 md:h-9 md:w-9 ${className}`}
    />
  );
}

const fieldClasses =
  'w-full rounded-lg border border-border bg-surface px-3 py-2 text-sm text-text outline-none transition-colors placeholder:text-faint focus:border-subtle';

export function Input({ className = '', ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`${fieldClasses} ${className}`} />;
}

export function Textarea({ className = '', ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={`${fieldClasses} ${className}`} />;
}

export function Select({ className = '', ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`${fieldClasses} ${className}`} />;
}

export function Dialog({
  open,
  onClose,
  title,
  children,
  wide = false,
  fixedBody = false,
  align = 'center',
  fullScreen = false,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  wide?: boolean;
  /** Keep the header fixed and scroll only the body (used by chat tools dialog). */
  fixedBody?: boolean;
  /** Anchor the dialog at a fixed distance from the top instead of centering. */
  align?: 'center' | 'top';
  /** Fill the viewport on mobile (bottom sheet becomes a full screen). */
  fullScreen?: boolean;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div
      className={`fixed inset-0 z-50 flex items-end justify-center bg-bg/70 sm:p-4 ${
        align === 'top' ? 'sm:items-start sm:pt-[12vh]' : 'sm:items-center'
      }`}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className={`w-full border border-border bg-bg shadow-2xl sm:rounded-xl ${
          fullScreen
            ? 'h-dvh max-h-dvh rounded-t-none pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] sm:h-auto sm:max-h-[85vh] sm:pt-5 sm:pb-5'
            : 'max-h-[85vh] rounded-t-2xl pb-5'
        } p-5 ${wide ? 'sm:max-w-2xl' : 'sm:max-w-md'} ${
          fixedBody ? 'flex flex-col' : 'overflow-y-auto'
        }`}
      >
        <div
          className={`mb-4 flex items-center justify-between gap-2 ${
            fixedBody ? 'shrink-0' : ''
          }`}
        >
          <h2 className="text-base font-semibold text-text">{title}</h2>
          <IconButton onClick={onClose} aria-label="Close" className="-mr-1 h-8 w-8 md:h-8 md:w-8">
            <IconX className="h-4 w-4" />
          </IconButton>
        </div>
        {fixedBody ? <div className="min-h-0 flex-1 overflow-y-auto">{children}</div> : children}
      </div>
    </div>
  );
}

export function Section({
  title,
  description,
  badge,
  children,
}: {
  title: string;
  description?: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-xl border border-border bg-surface p-4 md:p-5">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold text-text">{title}</h2>
        {badge}
      </div>
      {description && <p className="mt-1 text-xs text-subtle">{description}</p>}
      <div className="mt-4 flex flex-col gap-3">{children}</div>
    </section>
  );
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-subtle">{label}</span>
      {children}
    </label>
  );
}

export function SaveRow({
  saving,
  saved,
  error,
  extra,
}: {
  saving: boolean;
  saved: boolean;
  error: string | null;
  extra?: ReactNode;
}) {
  return (
    <div className="flex items-center gap-2">
      <Button type="submit" variant="outline" disabled={saving}>
        {saving ? <Spinner className="h-4 w-4" /> : 'Save'}
      </Button>
      {extra}
      {saved && !saving && (
        <span className="flex items-center gap-1 text-xs text-emerald-500">
          <IconCheck className="h-3.5 w-3.5" /> Saved
        </span>
      )}
      {error && <span className="text-xs text-red-400">{error}</span>}
    </div>
  );
}

export function Center({ children }: { children: ReactNode }) {
  return (
    <div className="v1-safe-top flex min-h-dvh items-center justify-center p-4">
      <div className="flex flex-col items-center gap-3">{children}</div>
    </div>
  );
}

export function ErrorBox({ message, className = '' }: { message: string; className?: string }) {
  return (
    <div
      className={`rounded-lg border border-red-900/60 bg-red-950/40 px-3 py-2 text-sm text-red-300 ${className}`}
    >
      {message}
    </div>
  );
}
