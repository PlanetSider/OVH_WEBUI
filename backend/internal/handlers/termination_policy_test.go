package handlers

import "testing"

func TestParseLifecycleTermination(t *testing.T) {
	cases := []struct {
		name       string
		actions    []string
		want       bool
		wantAction string
	}{
		{name: "none", actions: []string{"renew"}, want: false},
		{name: "expiration", actions: []string{TerminationAtExpiration}, want: true, wantAction: TerminationAtExpiration},
		{name: "immediate", actions: []string{"terminate"}, want: true, wantAction: "terminate"},
		{name: "engagement", actions: []string{TerminationAtEngagement}, want: true, wantAction: TerminationAtEngagement},
		{name: "legacy", actions: []string{"deleteAtExpiration"}, want: true, wantAction: "deleteAtExpiration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var info lifecycleTerminationResponse
			info.Billing.Lifecycle.Current.PendingActions = tc.actions
			info.Billing.Lifecycle.Current.TerminationDate = "2027-01-02T03:04:05Z"
			got, action, date := parseLifecycleTermination(info)
			if got != tc.want || action != tc.wantAction {
				t.Fatalf("parseLifecycleTermination() = (%v, %q), want (%v, %q)", got, action, tc.want, tc.wantAction)
			}
			if tc.want && date == "" {
				t.Fatal("scheduled termination lost termination date")
			}
		})
	}
}

func TestTerminationPolicyAllowlist(t *testing.T) {
	for _, policy := range []string{TerminationNone, TerminationAtExpiration, TerminationAtEngagement} {
		if !terminationPolicies[policy] {
			t.Fatalf("policy %q is not allowed", policy)
		}
	}
	if terminationPolicies["terminate"] {
		t.Fatal("immediate terminate must not be accepted as a termination policy")
	}
}
