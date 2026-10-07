import { Component, type ErrorInfo, type ReactNode } from 'react';
import { debugError } from '../debuglog';
import { Button } from './ui';

// The app previously had NO error boundary, so a single throw during render
// (for example while rendering a chat history) blanked the whole document with
// no trace in the diagnostics export. This catches those throws, records them,
// and shows the user something actionable.
type Props = { children: ReactNode };
type State = { error: Error | null; componentStack: string };

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null, componentStack: '' };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    const componentStack = info.componentStack ?? '';
    this.setState({ componentStack });
    debugError('react', error, { componentStack });
  }

  render(): ReactNode {
    const { error, componentStack } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="v1-safe-top flex min-h-dvh items-center justify-center p-4">
        <div className="w-full max-w-3xl rounded-xl border border-border bg-surface p-4">
          <h1 className="text-base font-semibold text-text">Something went wrong</h1>
          <p className="mt-1 text-sm text-dim">
            The app hit an unexpected error while rendering. Reload to try again.
          </p>
          <pre className="mt-3 max-h-64 overflow-auto overscroll-contain whitespace-pre-wrap rounded-lg border border-border bg-bg p-3 text-xs text-red-300">
            {error.name}: {error.message}
          </pre>
          {componentStack && (
            <pre className="mt-3 max-h-64 overflow-auto overscroll-contain whitespace-pre-wrap rounded-lg border border-border bg-bg p-3 text-xs text-dim">
              {componentStack}
            </pre>
          )}
          <div className="mt-4">
            <Button variant="primary" onClick={() => location.reload()}>
              Reload
            </Button>
          </div>
        </div>
      </div>
    );
  }
}
