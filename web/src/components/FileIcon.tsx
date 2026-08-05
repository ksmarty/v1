type TypeSpec = { bg: string; fg: string; label: string };

const TYPES: Record<string, TypeSpec> = {
  // Scripting / web
  js: { bg: '#f7df1e', fg: '#1e1e1e', label: 'JS' },
  mjs: { bg: '#f7df1e', fg: '#1e1e1e', label: 'JS' },
  cjs: { bg: '#f7df1e', fg: '#1e1e1e', label: 'JS' },
  jsx: { bg: '#61dafb', fg: '#1e1e1e', label: 'JSX' },
  ts: { bg: '#3178c6', fg: '#ffffff', label: 'TS' },
  tsx: { bg: '#3178c6', fg: '#ffffff', label: 'TSX' },
  json: { bg: '#cbcb41', fg: '#1e1e1e', label: '{}' },
  jsonc: { bg: '#cbcb41', fg: '#1e1e1e', label: '{}' },
  // Styles
  css: { bg: '#42a5f5', fg: '#ffffff', label: '#' },
  scss: { bg: '#cd6799', fg: '#ffffff', label: '#' },
  sass: { bg: '#cd6799', fg: '#ffffff', label: '#' },
  // Markup
  html: { bg: '#e44d26', fg: '#ffffff', label: '</>' },
  htm: { bg: '#e44d26', fg: '#ffffff', label: '</>' },
  xml: { bg: '#ff9800', fg: '#1e1e1e', label: '</>' },
  svg: { bg: '#ff9800', fg: '#1e1e1e', label: '</>' },
  // Docs / config
  md: { bg: '#519aba', fg: '#ffffff', label: 'M↓' },
  markdown: { bg: '#519aba', fg: '#ffffff', label: 'M↓' },
  yml: { bg: '#cb171e', fg: '#ffffff', label: 'Y' },
  yaml: { bg: '#cb171e', fg: '#ffffff', label: 'Y' },
  toml: { bg: '#9c4221', fg: '#ffffff', label: 'T' },
  ini: { bg: '#75715e', fg: '#ffffff', label: 'INI' },
  env: { bg: '#75715e', fg: '#ffffff', label: 'ENV' },
  sql: { bg: '#e38c00', fg: '#ffffff', label: 'DB' },
  // Languages
  py: { bg: '#3776ab', fg: '#ffd43b', label: 'PY' },
  go: { bg: '#00add8', fg: '#1e1e1e', label: 'GO' },
  rs: { bg: '#dea584', fg: '#1e1e1e', label: 'RS' },
  sh: { bg: '#89e051', fg: '#1e1e1e', label: '>_' },
  bash: { bg: '#89e051', fg: '#1e1e1e', label: '>_' },
  zsh: { bg: '#89e051', fg: '#1e1e1e', label: '>_' },
  // Tooling
  dockerfile: { bg: '#2496ed', fg: '#ffffff', label: 'D' },
  gitignore: { bg: '#f05033', fg: '#ffffff', label: 'GIT' },
  gitattributes: { bg: '#f05033', fg: '#ffffff', label: 'GIT' },
};

const FILE_PATTERNS: { re: RegExp; spec: TypeSpec }[] = [
  { re: /^dockerfile$/i, spec: TYPES.dockerfile },
  { re: /^makefile$/i, spec: { bg: '#e37933', fg: '#ffffff', label: 'MK' } },
  { re: /^\.gitignore$/, spec: TYPES.gitignore },
  { re: /^\.gitattributes$/, spec: TYPES.gitattributes },
  { re: /^readme/i, spec: TYPES.md },
  { re: /^license$/i, spec: { bg: '#8e44ad', fg: '#ffffff', label: 'LIC' } },
];

function specFor(name: string): TypeSpec | null {
  const base = name.split('/').pop() ?? name;
  for (const { re, spec } of FILE_PATTERNS) {
    if (re.test(base)) return spec;
  }
  const dot = base.lastIndexOf('.');
  if (dot <= 0) return null;
  return TYPES[base.slice(dot + 1).toLowerCase()] ?? null;
}

/** VSCode-extension-style colored file badge, or a plain document icon. */
export default function FileIcon({ name, className = '' }: { name: string; className?: string }) {
  const spec = specFor(name);
  if (!spec) {
    return (
      <svg
        viewBox="0 0 24 24"
        className={className}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.8}
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z" />
        <path d="M14 2v4a2 2 0 0 0 2 2h4" />
      </svg>
    );
  }
  const fs = spec.label.length <= 2 ? 8 : spec.label.length === 3 ? 6.5 : 5.5;
  return (
    <svg viewBox="0 0 16 16" className={className} aria-label={`${name} file icon`} role="img">
      <rect x="1" y="1" width="14" height="14" rx="3" fill={spec.bg} />
      <text
        x="8"
        y="8.6"
        textAnchor="middle"
        dominantBaseline="central"
        fontSize={fs}
        fontWeight="700"
        fontFamily="var(--font-sans)"
        fill={spec.fg}
      >
        {spec.label}
      </text>
    </svg>
  );
}
