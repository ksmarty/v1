import {
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react';
import { createPortal } from 'react-dom';
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

/**
 * A small `?` that reveals one paragraph of help on hover, focus or tap.
 *
 * Rendered in a portal with fixed coordinates: the settings cards sit in
 * scrolling, `overflow-hidden` columns, where an in-flow tooltip would be
 * clipped or would widen the row.
 */
export function InfoTip({ text, className = '' }: { text: string; className?: string }) {
  const ref = useRef<HTMLButtonElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number; width: number } | null>(null);

  const show = () => {
    const r = ref.current?.getBoundingClientRect();
    if (!r) return;
    const width = Math.min(288, window.innerWidth - 16);
    let left = r.left + r.width / 2 - width / 2;
    left = Math.max(8, Math.min(left, window.innerWidth - width - 8));
    setPos({ left, top: r.bottom + 6, width });
  };

  return (
    <>
      <button
        ref={ref}
        type="button"
        aria-label={text}
        onMouseEnter={show}
        onMouseLeave={() => setPos(null)}
        onFocus={show}
        onBlur={() => setPos(null)}
        onClick={show}
        className={`inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full border border-border-strong text-[10px] font-semibold leading-none text-faint transition-colors hover:border-subtle hover:text-subtle ${className}`}
      >
        ?
      </button>
      {pos &&
        createPortal(
          <span
            role="tooltip"
            style={{ left: pos.left, top: pos.top, width: pos.width }}
            className="pointer-events-none fixed z-[100] rounded-lg border border-border-strong bg-bg px-2.5 py-2 text-[11px] leading-relaxed text-subtle shadow-lg"
          >
            {text}
          </span>,
          document.body,
        )}
    </>
  );
}

const fieldClasses =
  'w-full min-w-0 rounded-lg border border-border bg-surface px-3 py-2 text-base text-text outline-none transition-colors placeholder:text-faint focus:border-subtle sm:text-sm';

export function Input({ className = '', ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input autoCorrect="off" {...props} className={`${fieldClasses} ${className}`} />;
}

export function Textarea({ className = '', ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea autoCorrect="off" {...props} className={`${fieldClasses} ${className}`} />;
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
  translucent = false,
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
  /** Semi-transparent, blurred panel with extra padding (tools dialog). */
  translucent?: boolean;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [open, onClose]);

  if (!open) return null;

  // Two different cases, deliberately treated differently.
  //
  // A fullscreen sheet fills the fixed overlay, whose top edge is the physical
  // screen top in iOS standalone. It keeps the safe-area inset so its title
  // clears the Dynamic Island, exactly like the app chrome (which uses
  // .v1-safe-top for the same reason).
  //
  // A bottom sheet does not fill the screen, so an inset would be dead space.
  // Its top padding is three quarters of its horizontal padding: the header row
  // is taller than its text, so matching the raw numbers still reads as a wider
  // gap above the title than beside it. Bottom sheets are anchored mid-screen,
  // so they only need the bottom inset for the home indicator.
  const pad = fullScreen
    ? translucent
      ? 'px-3 pt-[max(0.75rem,env(safe-area-inset-top))] pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-4'
      : 'px-4 pt-[max(1rem,env(safe-area-inset-top))] pb-[max(1rem,env(safe-area-inset-bottom))]'
    : translucent
      ? 'px-3 pt-2 pb-3 sm:px-4 sm:pt-3'
      : 'px-4 pt-3 pb-[max(1rem,env(safe-area-inset-bottom))]';
  // The header row is taller than its text — the close button is 32px against a
  // 24px line — so the title sits a few pixels below the padding edge. One step
  // less on the top makes the visible gap above the title match the gap beside
  // it, which is the comparison the eye actually makes.
  const desktopPad = translucent ? 'sm:px-6 sm:pb-8 sm:pt-5' : 'sm:px-5 sm:pt-4 sm:pb-5';

  // Portaled to document.body so the fixed overlay always uses the viewport as
  // its containing block — no ancestor (scroll container, stacking context) can
  // clip it or render it behind other content.
  const overlay = (
    <div
      className={`fixed inset-0 z-50 flex items-end justify-center bg-bg/70 sm:p-4 ${
        align === 'top' ? 'sm:items-start sm:pt-8' : 'sm:items-center'
      }`}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className={`w-full shadow-2xl sm:rounded-xl ${
          fullScreen ? 'border-0 sm:border sm:border-border' : 'border border-border'
        } ${
          translucent ? 'bg-bg/85 backdrop-blur-md' : 'bg-bg'
        } ${
          fullScreen
            ? `h-full ${pad} sm:h-auto sm:max-h-[85vh] ${desktopPad}`
            : `max-h-[85vh] rounded-t-2xl ${pad}`
        } ${wide ? 'sm:max-w-2xl' : 'sm:max-w-md'} ${
          fixedBody ? 'flex min-h-0 flex-col overflow-hidden' : 'overflow-y-auto'
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
        {fixedBody ? <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain">{children}</div> : children}
      </div>
    </div>
  );

  // Render into document.body so the overlay escapes every ancestor container.
  return createPortal(overlay, document.body);
}

export function Section({
  title,
  description,
  badge,
  children,
  id,
}: {
  title: string;
  description?: string;
  badge?: ReactNode;
  children: ReactNode;
  /** Anchor for settings deep links (scroll target). */
  id?: string;
}) {
  return (
    <section
      id={id}
      className="scroll-mt-16 rounded-xl border border-border bg-surface p-4 md:p-5"
    >
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

// Transient confirmations. A module-level queue rather than a context: the
// settings page alone has a dozen save rows, and threading a provider through
// every one of them just to say "Saved" is more machinery than the message is
// worth.
type ToastItem = { id: number; message: string };

let toastSeq = 0;
let toastItems: ToastItem[] = [];
const toastListeners = new Set<(items: ToastItem[]) => void>();

function publishToasts() {
  for (const listener of toastListeners) listener(toastItems);
}

/** Announce a completed action. Safe to call from anywhere. */
export function toast(message: string, ms = 2400) {
  const id = ++toastSeq;
  toastItems = [...toastItems, { id, message }];
  publishToasts();
  window.setTimeout(() => {
    toastItems = toastItems.filter((t) => t.id !== id);
    publishToasts();
  }, ms);
}

/** Renders the active toasts. Mount once, near the app root. */
export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([]);
  useEffect(() => {
    toastListeners.add(setItems);
    setItems(toastItems);
    return () => {
      toastListeners.delete(setItems);
    };
  }, []);
  if (items.length === 0) return null;
  return createPortal(
    <div className="pointer-events-none fixed inset-x-0 bottom-[max(1rem,env(safe-area-inset-bottom))] z-[70] flex flex-col items-center gap-2 px-4">
      {items.map((t) => (
        <div
          key={t.id}
          role="status"
          className="pointer-events-auto flex items-center gap-2 rounded-full border border-border-strong bg-bg px-3 py-1.5 text-xs text-text shadow-lg"
        >
          <IconCheck className="h-3.5 w-3.5 text-emerald-500" />
          {t.message}
        </div>
      ))}
    </div>,
    document.body,
  );
}

export function SaveRow({
  saving,
  saved,
  error,
  extra,
  pulse = false,
  disabled = false,
}: {
  saving: boolean;
  saved: boolean;
  error: string | null;
  extra?: ReactNode;
  /** Breathing accent glow on Save — set while there are unsaved changes. */
  pulse?: boolean;
  /** Form validation — the button stays off until required fields are valid. */
  disabled?: boolean;
}) {
  // A save is a completed action, so it is announced once instead of being
  // left as permanent text beside the button. Fires on the transition to saved,
  // so a row that mounts already-saved stays quiet.
  const wasSaved = useRef(false);
  useEffect(() => {
    if (saved && !wasSaved.current) toast('Saved');
    wasSaved.current = saved;
  }, [saved]);

  return (
    <div className="flex items-center gap-2">
      <Button
        type="submit"
        variant="outline"
        // Nothing to write until something has changed, so Save stays off
        // rather than offering a no-op round trip.
        disabled={saving || disabled || !pulse}
        className={pulse && !disabled ? 'v1-save-breathe' : ''}
      >
        {saving ? <Spinner className="h-4 w-4" /> : 'Save'}
      </Button>
      {extra}
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
