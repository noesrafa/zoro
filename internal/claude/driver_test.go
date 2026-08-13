package claude

import "testing"

// The strings below are VERBATIM from the CLI, captured by reproducing each
// failure against the real binary. The auth one is the whole point of Failure():
// it arrives on stdout as a <synthetic> assistant message while stderr stays
// empty, so without it the caller only ever sees "exit status 1".
func TestResultFailure(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want FailureKind
	}{
		{
			name: "oauth expired (stderr was empty)",
			res:  Result{Text: "Failed to authenticate: OAuth session expired and could not be refreshed", IsError: true},
			want: FailAuth,
		},
		{
			name: "resume of a missing session",
			res:  Result{Errors: []string{"No conversation found with session ID: 00000000-1111-2222-3333-444444444444"}},
			want: FailNoSession,
		},
		{
			name: "usage limit",
			res:  Result{Text: "Claude AI usage limit reached|1755100000"},
			want: FailLimit,
		},
		{
			name: "a normal answer is not a failure",
			res:  Result{Text: "No payments due today, hermano."},
			want: FailUnknown,
		},
		{
			name: "empty result",
			res:  Result{},
			want: FailUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.res.Failure(); got != c.want {
				t.Errorf("Failure() = %v, want %v (diagnostic %q)", got, c.want, c.res.Diagnostic())
			}
		})
	}
}

func TestDiagnosticJoinsTextAndErrors(t *testing.T) {
	r := Result{Text: "boom", Errors: []string{"detail one", "detail two"}}
	if got, want := r.Diagnostic(), "boom\ndetail one\ndetail two"; got != want {
		t.Errorf("Diagnostic() = %q, want %q", got, want)
	}
	if got := (Result{}).Diagnostic(); got != "" {
		t.Errorf("empty Result should have an empty diagnostic, got %q", got)
	}
}
