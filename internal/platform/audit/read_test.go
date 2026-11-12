package audit

import (
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestParseReadQuery(t *testing.T) {
	user := "018f3b2c-7a10-7c4a-8e11-1a2b3c4d5e6f"
	cases := []struct {
		name    string
		query   string
		want    readQuery
		wantErr bool
	}{
		{name: "no query", query: "", want: readQuery{limit: readDefaultLimit}},
		{name: "a page", query: "after=7&limit=3", want: readQuery{after: 7, limit: 3}},
		{name: "the most a page holds", query: "limit=1000", want: readQuery{limit: 1000}},
		{name: "a bad cursor", query: "after=-1", wantErr: true},
		{name: "a cursor that is not a number", query: "after=first", wantErr: true},
		{name: "no limit of zero", query: "limit=0", wantErr: true},
		{name: "no limit past the most", query: "limit=1001", wantErr: true},
		{name: "a limit that is not a number", query: "limit=many", wantErr: true},
		{
			name:  "every filter names itself",
			query: "action=auth.sign_out&actor_id=" + user + "&target_type=session&target_id=s1&outcome=denied&from=2026-11-12T10:00:00Z&to=2026-11-12T11:00:00Z",
			want: readQuery{
				limit: readDefaultLimit, action: "auth.sign_out", actorID: user,
				targetType: "session", targetID: "s1", outcome: "denied",
				from:    time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
				to:      time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC),
				filters: []string{"action", "actor_id", "target_type", "target_id", "outcome", "from", "to"},
			},
		},
		{name: "an actor that is not an id", query: "actor_id=everyone", wantErr: true},
		{name: "a from that is not a timestamp", query: "from=yesterday", wantErr: true},
		{name: "a to that is not RFC 3339", query: "to=2026-11-12", wantErr: true},
		{name: "an empty value is no filter", query: "action=&limit=1", want: readQuery{limit: 1}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values, err := url.ParseQuery(testCase.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseReadQuery(values)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("query %q parsed as %+v, want a client error", testCase.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("query %q: %v", testCase.query, err)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("query %q parsed as %+v, want %+v", testCase.query, got, testCase.want)
			}
		})
	}
}
