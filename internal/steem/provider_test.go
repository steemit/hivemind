package steem

import (
	"reflect"
	"testing"
)

// TestClientImplementsProvider is a runtime mirror of the compile-time
// `var _ Provider = (*Client)(nil)` assertion.
func TestClientImplementsProvider(t *testing.T) {
	var p Provider = (*Client)(nil)
	ifaceType := reflect.TypeOf((*Provider)(nil)).Elem()
	clientType := reflect.TypeOf(p)

	for i := 0; i < ifaceType.NumMethod(); i++ {
		want := ifaceType.Method(i)
		if _, ok := clientType.MethodByName(want.Name); !ok {
			t.Errorf("*steem.Client is missing Provider method %q", want.Name)
		}
	}
}

// TestProviderInterfaceSurface pins the exact method set of Provider so that
// adding/removing a method is a deliberate, reviewed change.
func TestProviderInterfaceSurface(t *testing.T) {
	want := map[string]bool{
		"GetBlock":                   true,
		"GetBlocksRange":             true,
		"GetAccounts":                true,
		"GetContent":                 true,
		"GetContentBatch":            true,
		"GetDynamicGlobalProperties": true,
		"HeadBlock":                  true,
		"LastIrreversible":           true,
	}
	ifaceType := reflect.TypeOf((*Provider)(nil)).Elem()
	got := make(map[string]bool, ifaceType.NumMethod())
	for i := 0; i < ifaceType.NumMethod(); i++ {
		got[ifaceType.Method(i).Name] = true
	}
	if len(got) != len(want) {
		t.Fatalf("Provider has %d methods, test expects %d — update this test", len(got), len(want))
	}
	for name := range want {
		if !got[name] {
			t.Errorf("Provider is missing expected method %q", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("Provider has unexpected method %q — update the test if intentional", name)
		}
	}
}
