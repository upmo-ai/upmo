//go:build integration

package kubernetes

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
)

func TestIntegrationCreateDestroy(t *testing.T) {
	if os.Getenv("UPMO_TEST_KUBECONFIG") == "" && os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		t.Skip("UPMO_TEST_KUBECONFIG or in-cluster config required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := Open(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	box, err := d.Create(ctx, runtime.Spec{
		Task:  "018f3b2c-7a10-7c4a-8e11-222222222222",
		Image: "busybox:1.36",
		Tier:  runtime.TierContainer,
		TTL:   5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Destroy(context.Background(), box.ID) })
	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
}
