package types

import (
	"testing"
	"time"
)

func TestParseTSAcceptsPersistedFormats(t *testing.T) {
	local := time.Local
	cases := []struct {
		name  string
		value string
		want  time.Time
	}{
		{
			name:  "now iso",
			value: "2026-09-22T13:44:26.123456",
			want:  time.Date(2026, 9, 22, 13, 44, 26, 123456000, local),
		},
		{
			name:  "rfc3339",
			value: "2026-09-22T05:44:26Z",
			want:  time.Date(2026, 9, 22, 5, 44, 26, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseTS(tc.value)
			if !ok {
				t.Fatalf("ParseTS(%q) did not parse", tc.value)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("ParseTS(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseTSRejectsInvalidValue(t *testing.T) {
	if got, ok := ParseTS("not-a-timestamp"); ok || !got.IsZero() {
		t.Fatalf("ParseTS invalid value = %v, %v; want zero, false", got, ok)
	}
}

func TestNowISORoundTripsThroughParseTS(t *testing.T) {
	original := time.Now()
	parsed, ok := ParseTS(original.Format(NowISOLayout))
	if !ok {
		t.Fatal("ParseTS did not parse a value produced by NowISOLayout")
	}
	if parsed.Sub(original).Abs() > time.Microsecond {
		t.Fatalf("round-trip delta = %s, want at most one microsecond", parsed.Sub(original).Abs())
	}
}
