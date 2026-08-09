package screenshot

import (
	"os"
	"testing"
)

func TestCaptureSmoke(t *testing.T) {
	if os.Getenv("V1_SMOKE") == "" {
		t.Skip("set V1_SMOKE=1 to run")
	}
	png, err := Capture(t.Context(), "http://127.0.0.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 10_000 {
		t.Fatalf("screenshot suspiciously small: %d bytes", len(png))
	}
	t.Logf("captured %d bytes", len(png))
}
