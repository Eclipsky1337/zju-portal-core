package daemonconfig

import (
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestChangesClassifiesConfigurationFields(t *testing.T) {
	active := Default()
	configured := active.Clone()
	configured.Session.AutoStart = !active.Session.AutoStart
	configured.Routing.Mode = core.RoutingModeGlobal
	configured.Log.Level = "debug"
	configured.DNS.Remote.Server = "10.0.0.1"
	configured.Inbounds.TUN.MTU = 1300

	changes := Changes(active, configured)
	want := map[string]ApplyRequirement{
		"log.level":         ApplyRequirementCoreRestart,
		"routing.mode":      ApplyRequirementLive,
		"dns.remote.server": ApplyRequirementSessionRestart,
		"inbounds.tun.mtu":  ApplyRequirementCoreRestart,
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %#v", changes)
	}
	for _, change := range changes {
		if want[change.Path] != change.Requires {
			t.Fatalf("change %q requires %q", change.Path, change.Requires)
		}
	}
}

func TestMergeJSONPreservesOmittedFieldsAndAppliesFalse(t *testing.T) {
	config := Default()
	config.ATrust.Server = "vpn.example.edu"

	merged, err := MergeJSON(config, []byte(`{"session":{"auto-reconnect":false},"routing":{"mode":"global"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Session.AutoReconnect {
		t.Fatal("auto-reconnect was not disabled")
	}
	if merged.Session.ID != config.Session.ID || merged.ATrust.Server != config.ATrust.Server {
		t.Fatalf("omitted fields changed: %#v", merged)
	}
	if merged.Routing.Mode != core.RoutingModeGlobal {
		t.Fatalf("routing mode = %q", merged.Routing.Mode)
	}
}

func TestMergeJSONRejectsUnknownField(t *testing.T) {
	_, err := MergeJSON(Default(), []byte(`{"session":{"unknown":true}}`))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
}
