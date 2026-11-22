package domain

import "testing"

func TestNextActionTable(t *testing.T) {
	cases := []struct {
		class   FailureClass
		retries int
		want    Action
	}{
		{ClassTransient, 1, ActionRetry},
		{ClassTransient, 0, ActionNext},
		{ClassRateLimit, 1, ActionRetry},
		{ClassRateLimit, 0, ActionNext},
		{ClassOverloaded, 1, ActionRetry},
		{ClassTimeout, 0, ActionNext},
		{ClassCredential, 9, ActionNext},
		{ClassModelAccess, 0, ActionNext},
		{ClassCaller, 9, ActionFail},
		{ClassCanceled, 9, ActionFail},
		{ClassInternal, 9, ActionFail},
		{FailureClass("unknown"), 1, ActionFail},
	}
	for _, tc := range cases {
		got := NextAction(tc.class, tc.retries)
		if got != tc.want {
			t.Fatalf("%s retries=%d: got %d want %d", tc.class, tc.retries, got, tc.want)
		}
	}
}
