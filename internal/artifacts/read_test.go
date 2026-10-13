package artifacts_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

type fakeCatalog struct {
	listed []artifacts.Record
	rec    artifacts.Record
	err    error
}

func (f *fakeCatalog) ListByTask(context.Context, artifacts.Access, string) ([]artifacts.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.listed, nil
}

func (f *fakeCatalog) Get(context.Context, artifacts.Access, string) (artifacts.Record, error) {
	if f.err != nil {
		return artifacts.Record{}, f.err
	}
	return f.rec, nil
}

type fakeBlobs struct {
	body string
	err  error
}

func (f fakeBlobs) Open(context.Context, string, string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestListForTaskNeedsAccess(t *testing.T) {
	_, err := artifacts.ListForTask(context.Background(), &fakeCatalog{}, artifacts.Access{}, taskID)
	if !errors.Is(err, artifacts.ErrArtifact) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenForDownloadServesHeaders(t *testing.T) {
	cat := &fakeCatalog{rec: artifacts.Record{ID: "018f3b2c-7a10-7c4a-8e11-444444444444", Name: "report.pdf", Store: artifacts.StoreVolume, Key: "task/report.pdf"}}
	rec, headers, body, err := artifacts.OpenForDownload(context.Background(), cat, fakeBlobs{body: "pdf"}, artifacts.Access{UserID: ownerUser, Allow: "artifact:view"}, cat.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if rec.Name != "report.pdf" || headers.Type != "application/pdf" || headers.Sniff != artifacts.SniffValue {
		t.Fatalf("rec=%#v headers=%#v", rec, headers)
	}
	if !strings.Contains(headers.Disposition, "inline") {
		t.Fatalf("disposition = %q", headers.Disposition)
	}
}

func TestOpenForDownloadObjectStoreIsBlob(t *testing.T) {
	cat := &fakeCatalog{rec: artifacts.Record{ID: "018f3b2c-7a10-7c4a-8e11-444444444444", Name: "a.txt", Store: artifacts.StoreObject, Key: "k"}}
	_, _, _, err := artifacts.OpenForDownload(context.Background(), cat, artifacts.Volume{Root: t.TempDir()}, artifacts.Access{UserID: ownerUser, Allow: "artifact:view"}, cat.rec.ID)
	if !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("error = %v", err)
	}
}
