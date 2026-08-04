/**
 * Runtime theming.
 *
 * The UI uses CSS variables (--v1-*) consumed through Tailwind tokens defined
 * with `@theme inline` in index.css. This module holds the palette data
 * (happyHues, embedded at build time — never fetched at runtime), derives a
 * full token set from a palette, and applies tokens to documentElement.
 */

export interface Palette {
  name: string;
  colors: string[];
}

export interface ThemeTokens {
  bg: string;
  surface: string;
  border: string;
  borderStrong: string;
  text: string;
  textDim: string;
  textSubtle: string;
  textFaint: string;
  accent: string;
  accentText: string;
  primary: string;
  primaryText: string;
}

export const THEME_STORAGE_KEY = 'v1.theme';
export const DEFAULT_THEME_NAME = 'v1 Dark';

/** Default theme — matches the original hardcoded UI colors exactly. */
export const V1_DARK: ThemeTokens = {
  bg: '#0a0a0a',
  surface: '#171717',
  border: '#262626',
  borderStrong: '#404040',
  text: '#e5e5e5',
  textDim: '#a3a3a3',
  textSubtle: '#737373',
  textFaint: '#525252',
  accent: '#3b82f6',
  accentText: '#ffffff',
  primary: '#ffffff',
  primaryText: '#000000',
};

/** The 17 happyHues palettes (https://github.com/meodai/happyHuesColors). */
export const PALETTES: Palette[] = [
  { name: "Mangala Nymph", colors: ["#fef6e4","#8bd3dd","#f582ae","#f3d2c1"] },
  { name: "Dark Fuzz", colors: ["#55423d","#e78fb3","#ffc0ad","#9656a1","#fff3ec","#271c19"] },
  { name: "White Piglet", colors: ["#faeee7","#ff8ba7","#ffc6c7","#c3f0ca","#fffffe"] },
  { name: "White Waters", colors: ["#fffffe","#ffd803","#e3f6f5","#bae8e8"] },
  { name: "Flattered Sugar", colors: ["#0f0e17","#ff8906","#f25f4c","#e53170","#fffffe"] },
  { name: "Midnight Evening", colors: ["#232946","#eebbc3","#fffffe","#b8c1ec","#d4d8f0"] },
  { name: "Whisper Mossy", colors: ["#f9f4ef","#8c7851","#eaddcf","#f25042","#fffffe"] },
  { name: "Fizzy Whirlpool", colors: ["#004643","#f9bc60","#abd1c6","#e16162","#e8e4e6"] },
  { name: "Paper White", colors: ["#eff0f3","#ff8e3c","#fffffe","#d9376e"] },
  { name: "Opal Teal", colors: ["#f8f5f2","#078080","#f45d48","#fffffe"] },
  { name: "Dreamy Candy Moon", colors: ["#fec7d7","#d9d4e7","#a786df","#f9f8fc","#fffffe"] },
  { name: "Meteor White", colors: ["#fffffe","#6246ea","#d1d1e9","#e45858"] },
  { name: "Corona Forest", colors: ["#f2f7f5","#faae2b","#ffa8ba","#fa5246","#00473e"] },
  { name: "Candy Grape White", colors: ["#16161a","#7f5af0","#72757e","#2cb67d","#fffffe","#242629"] },
  { name: "White On Melon", colors: ["#fffffe","#3da9fc","#90b4ce","#ef4565","#d8eefe"] },
  { name: "Cheese It White", colors: ["#fffffe","#00ebc7","#ff5470","#fde24f","#f2f4f6"] },
  { name: "White Goddess", colors: ["#fffffe","#4fc4cf","#994ff3","#fbdd74","#f2eef5","#f6efef"] },
];

// ---------- tiny hex-color utils ----------

function hexToRgb(hex: string): [number, number, number] {
  let h = hex.replace('#', '');
  if (h.length === 3) {
    h = h
      .split('')
      .map((c) => c + c)
      .join('');
  }
  const n = parseInt(h, 16);
  if (Number.isNaN(n)) return [0, 0, 0];
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

function rgbToHex(r: number, g: number, b: number): string {
  const c = (v: number) =>
    Math.round(Math.min(255, Math.max(0, v)))
      .toString(16)
      .padStart(2, '0');
  return `#${c(r)}${c(g)}${c(b)}`;
}

/** Relative luminance (WCAG). */
function luminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrastRatio(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  const [hi, lo] = la > lb ? [la, lb] : [lb, la];
  return (hi + 0.05) / (lo + 0.05);
}

/** Mix two colors; t = weight of `a`. */
function mix(a: string, b: string, t: number): string {
  const ca = hexToRgb(a);
  const cb = hexToRgb(b);
  return rgbToHex(
    ca[0] * t + cb[0] * (1 - t),
    ca[1] * t + cb[1] * (1 - t),
    ca[2] * t + cb[2] * (1 - t),
  );
}

/** Move `amount` (0..1) toward white. */
function lighten(hex: string, amount: number): string {
  return mix('#ffffff', hex, amount);
}

/** Move `amount` (0..1) toward black. */
function darken(hex: string, amount: number): string {
  return mix('#000000', hex, amount);
}

// ---------- palette → tokens ----------

export function deriveFromPalette(p: Palette): ThemeTokens {
  const bg = p.colors[0] ?? V1_DARK.bg;
  const accent = p.colors[1] ?? V1_DARK.accent;
  const dark = luminance(bg) < 0.5;

  let text: string;
  if (dark) {
    const lightest = [...p.colors].sort((a, b) => luminance(b) - luminance(a))[0];
    text = lightest && contrastRatio(lightest, bg) >= 6 ? lightest : '#ffffff';
  } else {
    const darkest = [...p.colors].sort((a, b) => luminance(a) - luminance(b))[0];
    text = darkest && contrastRatio(darkest, bg) >= 6 ? darkest : '#1a1a1a';
  }

  const accentText =
    contrastRatio('#ffffff', accent) >= contrastRatio('#1a1a1a', accent)
      ? '#ffffff'
      : '#1a1a1a';

  return {
    bg,
    surface: dark ? lighten(bg, 0.07) : darken(bg, 0.05),
    border: dark ? lighten(bg, 0.14) : darken(bg, 0.12),
    borderStrong: dark ? lighten(bg, 0.22) : darken(bg, 0.2),
    text,
    textDim: mix(text, bg, dark ? 0.72 : 0.62),
    textSubtle: mix(text, bg, dark ? 0.55 : 0.5),
    textFaint: mix(text, bg, dark ? 0.38 : 0.32),
    accent,
    accentText,
    primary: accent,
    primaryText: accentText,
  };
}

// ---------- apply / persist ----------

const VAR_MAP: Record<keyof ThemeTokens, string> = {
  bg: '--v1-bg',
  surface: '--v1-surface',
  border: '--v1-border',
  borderStrong: '--v1-border-strong',
  text: '--v1-text',
  textDim: '--v1-text-dim',
  textSubtle: '--v1-text-subtle',
  textFaint: '--v1-text-faint',
  accent: '--v1-accent',
  accentText: '--v1-accent-text',
  primary: '--v1-primary',
  primaryText: '--v1-primary-text',
};

export function tokensForTheme(name: string): ThemeTokens {
  if (name === DEFAULT_THEME_NAME) return V1_DARK;
  const p = PALETTES.find((x) => x.name === name);
  return p ? deriveFromPalette(p) : V1_DARK;
}

export function applyTheme(name: string): void {
  const tokens = tokensForTheme(name);
  const style = document.documentElement.style;
  for (const [key, varName] of Object.entries(VAR_MAP) as [keyof ThemeTokens, string][]) {
    style.setProperty(varName, tokens[key]);
  }
  // Keep native form controls / scrollbars in sync with the theme brightness.
  style.colorScheme = luminance(tokens.bg) < 0.5 ? 'dark' : 'light';
}

export function getStoredTheme(): string {
  try {
    return localStorage.getItem(THEME_STORAGE_KEY) ?? DEFAULT_THEME_NAME;
  } catch {
    return DEFAULT_THEME_NAME;
  }
}

export function setStoredTheme(name: string): void {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, name);
  } catch {
    // ignore (private mode etc.)
  }
}

/** Apply the persisted theme. Call once before rendering. */
export function initTheme(): void {
  applyTheme(getStoredTheme());
}

/** Options for the settings theme picker (default first). */
export function listThemeOptions(): { name: string; swatches: string[] }[] {
  return [
    {
      name: DEFAULT_THEME_NAME,
      swatches: [V1_DARK.bg, V1_DARK.accent, V1_DARK.border, V1_DARK.text],
    },
    ...PALETTES.map((p) => ({ name: p.name, swatches: p.colors.slice(0, 5) })),
  ];
}
