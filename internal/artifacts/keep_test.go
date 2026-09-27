package artifacts_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
	taskID    = "018f3b2c-7a10-7c4a-8e11-333333333333"
)

func TestKeepTrimsEveryArgument(t *testing.T) {
	got, err := artifacts.Keep(" "+ownerUser+" ", "\t"+taskID+"\n", "  report.pdf ", " volume ", " task/report.pdf ")
	if err != nil {
		t.Fatal(err)
	}
	want := artifacts.Artifact{
		Owner: ownerUser,
		Task:  taskID,
		Name:  "report.pdf",
		Store: artifacts.StoreVolume,
		Key:   "task/report.pdf",
	}
	if got != want {
		t.Fatalf("artifact = %#v, want %#v", got, want)
	}
}

func TestKeepAcceptsBothStores(t *testing.T) {
	for _, store := range []string{artifacts.StoreVolume, artifacts.StoreObject} {
		got, err := artifacts.Keep(ownerUser, taskID, "report.pdf", store, "task/report.pdf")
		if err != nil {
			t.Fatalf("%s: %v", store, err)
		}
		if got.Store != store {
			t.Fatalf("store = %q, want %q", got.Store, store)
		}
	}
}

func TestKeepNameLimits(t *testing.T) {
	longest := strings.Repeat("é", artifacts.NameRunes)
	if _, err := artifacts.Keep(ownerUser, taskID, longest, artifacts.StoreObject, "k"); err != nil {
		t.Fatalf("200 runes: %v", err)
	}
	if _, err := artifacts.Keep(ownerUser, taskID, "a", artifacts.StoreObject, "k"); err != nil {
		t.Fatalf("1 rune: %v", err)
	}
}

func TestKeepKeyLimits(t *testing.T) {
	for _, key := range []string{
		"k",
		strings.Repeat("a", 1024),
		"task/report.pdf",
		"task/..report",
		"task/report..pdf",
		"./task/report.pdf",
	} {
		if _, err := artifacts.Keep(ownerUser, taskID, "report.pdf", artifacts.StoreVolume, key); err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
	}
}

func TestKeepRefuses(t *testing.T) {
	cases := []struct {
		label                         string
		owner, task, name, store, key string
	}{
		{"blank owner", "  ", taskID, "n", "volume", "k"},
		{"bad owner", "not-a-uuid", taskID, "n", "volume", "k"},
		{"blank task", ownerUser, "", "n", "volume", "k"},
		{"bad task", ownerUser, "123", "n", "volume", "k"},
		{"blank name", ownerUser, taskID, "   ", "volume", "k"},
		{"long name", ownerUser, taskID, strings.Repeat("a", artifacts.NameRunes+1), "volume", "k"},
		{"blank store", ownerUser, taskID, "n", " ", "k"},
		{"unknown store", ownerUser, taskID, "n", "bucket", "k"},
		{"store case", ownerUser, taskID, "n", "Volume", "k"},
		{"blank key", ownerUser, taskID, "n", "volume", "  "},
		{"long key", ownerUser, taskID, "n", "volume", strings.Repeat("a", 1025)},
		{"absolute key", ownerUser, taskID, "n", "volume", "/etc/passwd"},
		{"backslash key", ownerUser, taskID, "n", "volume", `task\report.pdf`},
		{"parent key", ownerUser, taskID, "n", "volume", ".."},
		{"leading parent", ownerUser, taskID, "n", "volume", "../secret"},
		{"inner parent", ownerUser, taskID, "n", "volume", "task/../secret"},
		{"trailing parent", ownerUser, taskID, "n", "volume", "task/.."},
	}
	for _, c := range cases {
		got, err := artifacts.Keep(c.owner, c.task, c.name, c.store, c.key)
		if !errors.Is(err, artifacts.ErrArtifact) {
			t.Fatalf("%s: error = %v", c.label, err)
		}
		if got != (artifacts.Artifact{}) {
			t.Fatalf("%s: artifact = %#v", c.label, got)
		}
	}
}

func TestReleaseLeavesTheKey(t *testing.T) {
	a, err := artifacts.Keep(ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got := artifacts.Release(a)
	if got != a {
		t.Fatalf("released = %#v, want %#v", got, a)
	}
	if got.Key != "task/report.pdf" {
		t.Fatalf("key = %q", got.Key)
	}
}

func TestEraseSplitsByOwnerInOrder(t *testing.T) {
	items := []artifacts.Artifact{
		{Owner: ownerUser, Task: taskID, Name: "a", Store: "volume", Key: "a"},
		{Owner: otherUser, Task: taskID, Name: "b", Store: "object", Key: "b"},
		{Owner: ownerUser, Task: taskID, Name: "c", Store: "object", Key: "c"},
		{Owner: otherUser, Task: taskID, Name: "d", Store: "volume", Key: "d"},
	}
	kept, keys, err := artifacts.Erase(items, " "+ownerUser+" ")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"a", "c"}) {
		t.Fatalf("keys = %#v", keys)
	}
	if !reflect.DeepEqual(kept, []artifacts.Artifact{items[1], items[3]}) {
		t.Fatalf("kept = %#v", kept)
	}
}

func TestEraseLeavesTheCallerSlice(t *testing.T) {
	items := make([]artifacts.Artifact, 3, 8)
	items[0] = artifacts.Artifact{Owner: ownerUser, Key: "a"}
	items[1] = artifacts.Artifact{Owner: otherUser, Key: "b"}
	items[2] = artifacts.Artifact{Owner: otherUser, Key: "c"}
	before := append([]artifacts.Artifact(nil), items[:cap(items)]...)

	kept, _, err := artifacts.Erase(items, ownerUser)
	if err != nil {
		t.Fatal(err)
	}
	kept = append(kept, artifacts.Artifact{Owner: ownerUser, Key: "z"})
	kept[0].Key = "changed"

	if !reflect.DeepEqual(items[:cap(items)], before) {
		t.Fatalf("caller backing array = %#v, want %#v", items[:cap(items)], before)
	}
	if len(items) != 3 || cap(items) != 8 {
		t.Fatalf("caller len %d cap %d", len(items), cap(items))
	}
}

func TestEraseRefusesABadOwner(t *testing.T) {
	items := []artifacts.Artifact{{Owner: ownerUser, Key: "a"}}
	for _, owner := range []string{"", "   ", "not-a-uuid"} {
		kept, keys, err := artifacts.Erase(items, owner)
		if !errors.Is(err, artifacts.ErrArtifact) {
			t.Fatalf("owner %q: error = %v", owner, err)
		}
		if kept != nil || keys != nil {
			t.Fatalf("owner %q: kept = %#v keys = %#v", owner, kept, keys)
		}
	}
}
