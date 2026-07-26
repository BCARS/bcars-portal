package version_test

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/bcars/bcars-portal/internal/version"
)

func TestGet_PopulatesGoVersion(t *testing.T) {
	got := version.Get()
	if got.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
	if got.Version == "" {
		t.Fatalf("Version must not be empty; got %#v", got)
	}
}

func TestGet_IsCached(t *testing.T) {
	// Two calls should return byte-identical structs.
	a := version.Get()
	b := version.Get()
	if a != b {
		t.Fatalf("Get() returned different snapshots:\n a=%#v\n b=%#v", a, b)
	}
}

func TestShort_FormatShape(t *testing.T) {
	i := version.Info{Version: "v1.2.3", RevisionShort: "abcdef012345", Modified: false}
	if got := i.Short(); got != "v1.2.3+abcdef012345" {
		t.Fatalf("Short() = %q", got)
	}

	i.Modified = true
	if got := i.Short(); got != "v1.2.3+abcdef012345-dirty" {
		t.Fatalf("Short() dirty = %q", got)
	}

	bare := version.Info{Version: "dev"}
	if got := bare.Short(); got != "dev" {
		t.Fatalf("Short() bare = %q", got)
	}
}

func TestLong_ContainsExpectedFields(t *testing.T) {
	i := version.Info{
		Version:       "v1.0.0",
		Revision:      "0123456789abcdef0123456789abcdef01234567",
		RevisionShort: "0123456789ab",
		Time:          "2026-07-26T13:00:00Z",
		Module:        "github.com/bcars/bcars-portal",
		GoVersion:     "go1.26.0",
	}
	out := i.Long()
	for _, want := range []string{
		"bcars-portal v1.0.0+0123456789ab",
		"module:   github.com/bcars/bcars-portal",
		"revision: 0123456789abcdef0123456789abcdef01234567",
		"built:    2026-07-26T13:00:00Z",
		"go:       go1.26.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Long() missing %q; full output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "modified") {
		t.Errorf("Long() should not mention modified when clean; got:\n%s", out)
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	i := version.Info{
		Version:       "v1.0.0",
		Revision:      "abc",
		RevisionShort: "abc",
		GoVersion:     "go1.26.0",
	}
	raw := i.JSON()
	var got version.Info
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, raw)
	}
	if got != i {
		t.Fatalf("round-trip mismatch\n got=%#v\nwant=%#v", got, i)
	}
}
