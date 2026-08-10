// Package screenshot captures PNG screenshots of web pages with a shared
// headless Chrome instance (Chrome DevTools Protocol via chromedp). Chrome is
// located automatically; V1_CHROME_PATH overrides the binary path.
package screenshot

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

var (
	mu       sync.Mutex // serializes captures; Chrome is shared
	allocCtx context.Context
	browser  context.Context
	started  bool
	startErr error
)

// ensureBrowser lazily starts the shared headless Chrome. The first call pays
// the launch cost; later calls reuse the running browser.
func ensureBrowser() (context.Context, error) {
	if started {
		return browser, startErr
	}
	started = true
	opts := append([]chromedp.ExecAllocatorOption{},
		chromedp.DefaultExecAllocatorOptions[:]...,
	)
	opts = append(opts,
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	if p := os.Getenv("V1_CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	allocCtx, _ = chromedp.NewExecAllocator(context.Background(), opts...)
	browser, _ = chromedp.NewContext(allocCtx)
	startErr = chromedp.Run(browser)
	return browser, startErr
}

// Capture navigates to url in a fresh tab at 1280x800 and returns a PNG
// screenshot of the viewport.
func Capture(ctx context.Context, url string) ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	b, err := ensureBrowser()
	if err != nil {
		return nil, fmt.Errorf("starting headless Chrome (set V1_CHROME_PATH to a Chrome/Chromium binary): %w", err)
	}
	tabCtx, cancel := chromedp.NewContext(b)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(tabCtx, 30*time.Second)
	defer cancelTimeout()
	var png []byte
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1280, 800),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		// SPA dev servers render asynchronously — give the app a moment to
		// mount and run its first effects before shooting.
		chromedp.Sleep(1500*time.Millisecond),
		chromedp.CaptureScreenshot(&png),
	); err != nil {
		return nil, err
	}
	return png, nil
}

// RenderText navigates to url in a fresh tab and returns the rendered page's
// HTML (document.documentElement.outerHTML) after the app has had a moment
// to mount and hydrate — for extracting text from JS-rendered pages whose
// static response carries no content.
func RenderText(ctx context.Context, url string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	b, err := ensureBrowser()
	if err != nil {
		return "", fmt.Errorf("starting headless Chrome (set V1_CHROME_PATH to a Chrome/Chromium binary): %w", err)
	}
	tabCtx, cancel := chromedp.NewContext(b)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(tabCtx, 30*time.Second)
	defer cancelTimeout()
	var html string
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1280, 800),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2000*time.Millisecond),
		chromedp.Evaluate(`document.documentElement.outerHTML`, &html),
	); err != nil {
		return "", err
	}
	return html, nil
}
