package decisions

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

const surfaceUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var surfaceHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestPrefilterRejectsInvalidSurfacesBeforeKeeping(t *testing.T) {
	thirtyThree := make([]string, 33)
	for i := range thirtyThree {
		thirtyThree[i] = fmt.Sprintf("tool_%02d", i)
	}

	tests := []struct {
		name     string
		surfaces []Surface
	}{
		{
			name: "blank prompt",
			surfaces: []Surface{{
				Prompt:  " \t ",
				Offered: []string{"search_memory", "list_tasks"},
				Called:  []string{"search_memory"},
			}},
		},
		{
			name: "called tool not offered",
			surfaces: []Surface{{
				Prompt:  "Find memory",
				Offered: []string{"search_memory"},
				Called:  []string{"list_tasks"},
			}},
		},
		{
			name: "duplicate offered names",
			surfaces: []Surface{{
				Prompt:  "Find memory",
				Offered: []string{"search_memory", "search_memory"},
				Called:  []string{"search_memory"},
			}},
		},
		{
			name: "tool name with space",
			surfaces: []Surface{{
				Prompt:  "Find memory",
				Offered: []string{"search memory", "list_tasks"},
				Called:  []string{"list_tasks"},
			}},
		},
		{
			name: "thirty three offered names",
			surfaces: []Surface{{
				Prompt:  "Find memory",
				Offered: thirtyThree,
				Called:  []string{"tool_00"},
			}},
		},
		{
			name: "set calls nothing",
			surfaces: []Surface{{
				Prompt:  "Find memory",
				Offered: []string{"search_memory", "list_tasks"},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			book := &surfaceGates{}
			filter := &surfaceFilter{}
			err := Prefilter(
				context.Background(),
				book,
				surfaceAccess(),
				surfaceHops,
				test.surfaces,
				filter,
			)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("error = %v", err)
			}
			if filter.calls != 0 {
				t.Fatalf("filter calls = %d", filter.calls)
			}
			if len(book.saved) != 0 {
				t.Fatalf("saved = %+v", book.saved)
			}
		})
	}
}

func TestPrefilterRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &surfaceGates{}
	filter := &surfaceFilter{}
	access := Access{UserID: surfaceUser, Allow: "task:edit"}
	err := Prefilter(context.Background(), book, access, surfaceHops, oneSurface(), filter)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if filter.calls != 0 {
		t.Fatalf("filter calls = %d", filter.calls)
	}
}

func TestPrefilterSkipsFilterForStoredFingerprint(t *testing.T) {
	fp, err := Fingerprint(Config{Name: RouteSurface, Kind: KindFilter, Hops: surfaceHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &surfaceGates{fingerprint: fp, accepted: true}
	filter := &surfaceFilter{}
	if err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		oneSurface(),
		filter,
	); err != nil {
		t.Fatal(err)
	}
	if filter.calls != 0 {
		t.Fatalf("filter calls = %d", filter.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestAcceptRejectsFilterKindWithoutRunning(t *testing.T) {
	book := &surfaceGates{}
	run := &surfaceRunner{}
	err := Accept(
		context.Background(),
		book,
		surfaceAccess(),
		Config{Name: RouteSurface, Kind: KindFilter, Hops: surfaceHops},
		100,
		[]Example{{Prompt: "Choose tools"}},
		run,
	)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestPrefilterStoresPassingSurface(t *testing.T) {
	book := &surfaceGates{}
	filter := &surfaceFilter{kept: [][]string{{"search_memory"}}}
	if err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		oneSurface(),
		filter,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteSurface || got.Kind != KindFilter || got.Hit != 1 || got.Total != 1 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestPrefilterRejectsDroppedCalledTool(t *testing.T) {
	book := &surfaceGates{}
	filter := &surfaceFilter{kept: [][]string{{"list_tasks"}}}
	err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		oneSurface(),
		filter,
	)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestPrefilterRejectsKeptToolThatWasNotOffered(t *testing.T) {
	book := &surfaceGates{}
	filter := &surfaceFilter{kept: [][]string{{"search_memory", "made_up"}}}
	err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		oneSurface(),
		filter,
	)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestPrefilterStoresEveryPassingSurface(t *testing.T) {
	book := &surfaceGates{}
	filter := &surfaceFilter{kept: [][]string{
		{"search_memory"},
		{"list_tasks"},
	}}
	surfaces := []Surface{
		{
			Prompt:  "Find memory",
			Offered: []string{"search_memory", "list_tasks"},
			Called:  []string{"search_memory"},
		},
		{
			Prompt:  "List tasks",
			Offered: []string{"search_memory", "list_tasks"},
			Called:  []string{"list_tasks"},
		},
	}
	if err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		surfaces,
		filter,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Hit != 2 || got.Total != 2 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestPrefilterReturnsFilterErrorWithoutSaving(t *testing.T) {
	want := errors.New("filter failed")
	book := &surfaceGates{}
	filter := &surfaceFilter{err: want}
	err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		oneSurface(),
		filter,
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestPrefilterTrimsWithoutMutatingOfferedTools(t *testing.T) {
	book := &surfaceGates{}
	surfaces := []Surface{{
		Prompt:  "  Find memory \t",
		Offered: []string{"  search_memory \t", "list_tasks"},
		Called:  []string{" search_memory "},
	}}
	wantFirst := surfaces[0].Offered[0]
	filter := &surfaceFilter{
		keep: func(_ context.Context, _ string, offered []string) ([]string, error) {
			offered[0] = "made_up"
			return []string{" search_memory ", "search_memory"}, nil
		},
	}
	if err := Prefilter(
		context.Background(),
		book,
		surfaceAccess(),
		surfaceHops,
		surfaces,
		filter,
	); err != nil {
		t.Fatal(err)
	}
	if surfaces[0].Offered[0] != wantFirst {
		t.Fatalf("offered tool mutated to %q", surfaces[0].Offered[0])
	}
	if len(filter.prompts) != 1 || filter.prompts[0] != "Find memory" {
		t.Fatalf("filter prompts = %q", filter.prompts)
	}
	if len(filter.offered) != 1 ||
		len(filter.offered[0]) != 2 ||
		filter.offered[0][0] != "search_memory" ||
		filter.offered[0][1] != "list_tasks" {
		t.Fatalf("filter offered = %v", filter.offered)
	}
}

func oneSurface() []Surface {
	return []Surface{{
		Prompt:  "Find memory",
		Offered: []string{"search_memory", "list_tasks"},
		Called:  []string{"search_memory"},
	}}
}

func surfaceAccess() Access {
	return Access{UserID: surfaceUser, Allow: "settings:manage"}
}

type surfaceGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *surfaceGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *surfaceGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type surfaceFilter struct {
	kept    [][]string
	err     error
	keep    func(context.Context, string, []string) ([]string, error)
	calls   int
	prompts []string
	offered [][]string
}

func (f *surfaceFilter) Keep(ctx context.Context, prompt string, offered []string) ([]string, error) {
	call := f.calls
	f.calls++
	f.prompts = append(f.prompts, prompt)
	f.offered = append(f.offered, append([]string(nil), offered...))
	if f.keep != nil {
		return f.keep(ctx, prompt, offered)
	}
	if f.err != nil {
		return nil, f.err
	}
	if call < len(f.kept) {
		return append([]string(nil), f.kept[call]...), nil
	}
	return append([]string(nil), offered...), nil
}

type surfaceRunner struct {
	calls int
}

func (r *surfaceRunner) Decide(context.Context, string, []string) (bool, string, error) {
	r.calls++
	return true, "", nil
}
