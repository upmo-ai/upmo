package artifacts_test

import (
	"bytes"
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

type countingBlobs struct {
	body   string
	opens  int
	refuse bool
}

func (f *countingBlobs) Open(context.Context, string, string) (io.ReadCloser, error) {
	f.opens++
	if f.refuse {
		return nil, errors.New("opened")
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func viewAccess() artifacts.Access {
	return artifacts.Access{UserID: ownerUser, Allow: "artifact:view"}
}

func TestRead_UPM222_OpenForText(t *testing.T) {
	ctx := context.Background()
	access := viewAccess()
	for _, kind := range []string{"text", "markdown", "csv", "code", "html"} {
		cat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "a." + kind, Kind: kind, Size: 4}}
		blobs := &countingBlobs{body: "note"}
		_, raw, err := artifacts.OpenForText(ctx, cat, blobs, access, artID)
		if err != nil || string(raw) != "note" || blobs.opens != 1 {
			t.Fatalf("%s: raw %q err %v opens %d", kind, raw, err, blobs.opens)
		}
	}
	svg := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "icon.svg", Kind: artifacts.KindFor("icon.svg"), Size: 5}}
	if svg.rec.Kind != "code" {
		t.Fatalf("svg kind = %q", svg.rec.Kind)
	}
	blobs := &countingBlobs{body: "<svg>"}
	_, raw, err := artifacts.OpenForText(ctx, svg, blobs, access, artID)
	if err != nil || string(raw) != "<svg>" {
		t.Fatalf("svg raw %q err %v", raw, err)
	}

	for _, kind := range []string{"image", "pdf", "binary", "diff"} {
		cat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "a.bin", Kind: kind, Size: 1}}
		blobs := &countingBlobs{body: "x", refuse: true}
		_, _, err := artifacts.OpenForText(ctx, cat, blobs, access, artID)
		if !errors.Is(err, artifacts.ErrKind) || blobs.opens != 0 {
			t.Fatalf("%s: err %v opens %d", kind, err, blobs.opens)
		}
	}

	huge := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "big.txt", Kind: "text", Size: artifacts.MaxViewBytes + 1}}
	closed := &countingBlobs{body: "tiny", refuse: true}
	if _, _, err := artifacts.OpenForText(ctx, huge, closed, access, artID); !errors.Is(err, artifacts.ErrTooLarge) || closed.opens != 0 {
		t.Fatalf("stored size: err %v opens %d", err, closed.opens)
	}

	over := bytes.Repeat([]byte("a"), artifacts.MaxViewBytes+1)
	cat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "big.txt", Kind: "text", Size: 1}}
	stream := &countingBlobs{body: string(over)}
	if _, _, err := artifacts.OpenForText(ctx, cat, stream, access, artID); !errors.Is(err, artifacts.ErrTooLarge) || stream.opens != 1 {
		t.Fatalf("stream: err %v opens %d", err, stream.opens)
	}

	bad := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "bad.txt", Kind: "text", Size: 1}}
	if _, _, err := artifacts.OpenForText(ctx, bad, &countingBlobs{body: "\xff"}, access, artID); !errors.Is(err, artifacts.ErrKind) {
		t.Fatalf("utf-8: %v", err)
	}
}

func TestRead_UPM222_OpenForDiff(t *testing.T) {
	ctx := context.Background()
	access := viewAccess()
	valid := `{"files":[{"path":"a.txt","before":"old","after":"new"},{"path":"b.txt","before":null,"after":"created"}]}`
	cat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "changes.diff", Kind: "diff", Size: int64(len(valid))}}
	_, diff, err := artifacts.OpenForDiff(ctx, cat, &countingBlobs{body: valid}, access, artID)
	if err != nil || len(diff.Files) != 2 || diff.Files[0].Path != "a.txt" || diff.Files[0].Before == nil || *diff.Files[0].Before != "old" {
		t.Fatalf("diff %#v err %v", diff, err)
	}
	empty := `{"files":[]}`
	emptyCat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "empty.diff", Kind: "diff", Size: int64(len(empty))}}
	_, diff, err = artifacts.OpenForDiff(ctx, emptyCat, &countingBlobs{body: empty + "\n"}, access, artID)
	if err != nil || diff.Files == nil || len(diff.Files) != 0 {
		t.Fatalf("empty %#v err %v", diff, err)
	}

	text := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "a.txt", Kind: "text", Size: 1}}
	closed := &countingBlobs{refuse: true}
	if _, _, err := artifacts.OpenForDiff(ctx, text, closed, access, artID); !errors.Is(err, artifacts.ErrKind) || closed.opens != 0 {
		t.Fatalf("kind: err %v opens %d", err, closed.opens)
	}

	huge := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "big.diff", Kind: "diff", Size: artifacts.MaxViewBytes + 1}}
	if _, _, err := artifacts.OpenForDiff(ctx, huge, closed, access, artID); !errors.Is(err, artifacts.ErrTooLarge) || closed.opens != 0 {
		t.Fatalf("stored size: err %v opens %d", err, closed.opens)
	}
	over := bytes.Repeat([]byte("a"), artifacts.MaxViewBytes+1)
	small := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "big.diff", Kind: "diff", Size: 1}}
	stream := &countingBlobs{body: string(over)}
	if _, _, err := artifacts.OpenForDiff(ctx, small, stream, access, artID); !errors.Is(err, artifacts.ErrTooLarge) {
		t.Fatalf("stream: %v", err)
	}

	for _, body := range []string{
		`{`,
		`{"files":[]} {"files":[]}`,
		`{"files":null}`,
		`{}`,
		`{"files":[{"path":"","before":"a"}]}`,
		`{"files":[{"path":"   ","before":"a"}]}`,
		`{"files":[{"path":"a","before":null,"after":null}]}`,
		`{"files":[{"path":"a"}]}`,
	} {
		bad := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "bad.diff", Kind: "diff", Size: int64(len(body))}}
		if _, _, err := artifacts.OpenForDiff(ctx, bad, &countingBlobs{body: body}, access, artID); !errors.Is(err, artifacts.ErrKind) {
			t.Fatalf("body %q: %v", body, err)
		}
	}
}

func TestOpenForDownloadObjectStoreIsBlob(t *testing.T) {
	cat := &fakeCatalog{rec: artifacts.Record{ID: "018f3b2c-7a10-7c4a-8e11-444444444444", Name: "a.txt", Store: artifacts.StoreObject, Key: "k"}}
	_, _, _, err := artifacts.OpenForDownload(context.Background(), cat, artifacts.Volume{Root: t.TempDir()}, artifacts.Access{UserID: ownerUser, Allow: "artifact:view"}, cat.rec.ID)
	if !errors.Is(err, artifacts.ErrBlob) {
		t.Fatalf("error = %v", err)
	}
}
