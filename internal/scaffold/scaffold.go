// Package scaffold writes new-project template files.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Apply writes the template's files into dir. The project name is used in
// package.json / titles.
func Apply(dir, template, projectName string) error {
	files, err := files(template, projectName)
	if err != nil {
		return err
	}
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func files(template, name string) (map[string]string, error) {
	switch template {
	case "vite-react":
		return viteReact(name), nil
	case "static":
		return staticSite(name), nil
	case "empty":
		return map[string]string{
			"README.md": "# " + name + "\n",
		}, nil
	default:
		return nil, fmt.Errorf("unknown template %q (expected vite-react, static or empty)", template)
	}
}

// sanitizeName produces an npm-safe package name.
func sanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return "v1-app"
	}
	return out
}

func viteReact(name string) map[string]string {
	pkgName := sanitizeName(name)
	return map[string]string{
		"package.json": `{
  "name": "` + pkgName + `",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^18.3.1",
    "react-dom": "^18.3.1"
  },
  "devDependencies": {
    "@types/react": "^18.3.12",
    "@types/react-dom": "^18.3.1",
    "@vitejs/plugin-react": "^4.3.4",
    "typescript": "^5.6.3",
    "vite": "^5.4.11"
  }
}
`,
		"vite.config.ts": `import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
  },
})
`,
		"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "isolatedModules": true,
    "moduleDetection": "force",
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true
  },
  "include": ["src"]
}
`,
		"index.html": `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>` + name + `</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
`,
		"src/main.tsx": `import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
`,
		"src/App.tsx": `export default function App() {
  return (
    <main className="app">
      <h1>` + name + `</h1>
      <p>Your new app is ready. Ask v1 to build something.</p>
    </main>
  )
}
`,
		"src/index.css": `:root {
  color-scheme: light dark;
  font-family: system-ui, -apple-system, sans-serif;
}

body {
  margin: 0;
}

.app {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
}
`,
	}
}

func staticSite(name string) map[string]string {
	return map[string]string{
		"index.html": `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>` + name + `</title>
    <link rel="stylesheet" href="style.css" />
  </head>
  <body>
    <main>
      <h1>` + name + `</h1>
      <p>Your new site is ready. Ask v1 to build something.</p>
    </main>
    <script src="script.js"></script>
  </body>
</html>
`,
		"style.css": `body {
  margin: 0;
  font-family: system-ui, -apple-system, sans-serif;
}

main {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
}
`,
		"script.js": `console.log('` + name + ` loaded');
`,
	}
}
