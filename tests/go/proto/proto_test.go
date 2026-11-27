package proto_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/schema"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// contracts maps every contract to the type its examples decode into and the version this
// build reads.
var contracts = map[string]struct {
	title   string
	version int
	value   func() any
}{
	"agent-event":       {"AgentEvent", proto.AgentEventVersion, func() any { return new(proto.AgentEvent) }},
	"dotfiles-manifest": {"DotfilesManifest", proto.DotfilesManifestVersion, func() any { return new(proto.DotfilesManifest) }},
	"gateway-event":     {"GatewayEvent", proto.GatewayEventVersion, func() any { return new(proto.GatewayStatus) }},
	"gateway-health":    {"GatewayHealth", proto.GatewayHealthVersion, func() any { return new(proto.GatewayHealth) }},
	"gateway-status":    {"GatewayStatus", proto.GatewayStatusVersion, func() any { return new(proto.GatewayStatus) }},
	"host-config":       {"HostConfig", proto.HostConfigVersion, func() any { return new(proto.HostConfig) }},
	"host-secrets":      {"HostSecrets", proto.HostSecretsVersion, func() any { return new(proto.HostSecrets) }},
	"hub-config":        {"HubConfig", proto.HubConfigVersion, func() any { return new(proto.HubConfig) }},
	"hub-secrets":       {"HubSecrets", proto.HubSecretsVersion, func() any { return new(proto.HubSecrets) }},
	"hub-state":         {"HubState", proto.HubStateVersion, func() any { return new(proto.HubState) }},
	"hub-status":        {"HubStatus", proto.HubStatusVersion, func() any { return new(proto.HubStatus) }},
	"hub-wake":          {"HubWake", proto.HubWakeVersion, func() any { return new(proto.HubWake) }},
	"hub-wake-response": {"HubWakeResponse", proto.HubWakeResponseVersion, func() any { return new(proto.HubWakeResponse) }},
	"push-payload":      {"PushPayload", proto.PushPayloadVersion, func() any { return new(proto.PushPayload) }},
	"screen-frame":      {"ScreenFrame", proto.ScreenFrameVersion, func() any { return new(proto.ScreenFrame) }},
	"session":           {"Session", proto.SessionVersion, func() any { return new(proto.Session) }},
	"sync-progress":     {"SyncProgress", proto.SyncProgressVersion, func() any { return new(proto.SyncProgress) }},
}

// generic decodes JSON keeping numbers exact, so 1 and 1.0 stay different.
func generic(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// redact replaces the values of secret fields, which never serialize, in an example.
func redact(contract string, value any) {
	if contract != "host-secrets" && contract != "hub-secrets" {
		return
	}
	object := value.(map[string]any)
	for key := range object {
		if key != "version" && key != "vapid_public_key" {
			object[key] = "***"
		}
	}
}

func examples(t *testing.T) map[string][]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repo.Root(t), "contracts/examples/*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	byContract := map[string][]string{}
	for _, path := range paths {
		contract, _, _ := strings.Cut(filepath.Base(path), ".")
		byContract[contract] = append(byContract[contract], path)
	}
	return byContract
}

func TestEveryContractHasATypeAndAnExample(t *testing.T) {
	schemas, _ := filepath.Glob(filepath.Join(repo.Root(t), "contracts/*.schema.json"))
	found := examples(t)
	if len(schemas) != len(contracts) {
		t.Errorf("%d schema files, %d contracts known to this test", len(schemas), len(contracts))
	}
	for _, path := range schemas {
		name := strings.TrimSuffix(filepath.Base(path), ".schema.json")
		if _, ok := contracts[name]; !ok {
			t.Errorf("%s: no type registered in this test", name)
		}
		if len(found[name]) == 0 {
			t.Errorf("%s: no example in contracts/examples/", name)
		}
		var doc struct {
			Title   string `json:"title"`
			Version int    `json:"x-version"`
		}
		data, _ := os.ReadFile(path)
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if c := contracts[name]; doc.Title != c.title || doc.Version != c.version {
			t.Errorf("%s: schema says %s v%d, generated code says %s v%d", name, doc.Title, doc.Version, c.title, c.version)
		}
	}
	for name := range found {
		if _, ok := contracts[name]; !ok {
			t.Errorf("contracts/examples/%s.*.json has no schema", name)
		}
	}
}

func TestExamplesRoundTrip(t *testing.T) {
	for contract, paths := range examples(t) {
		for _, path := range paths {
			t.Run(filepath.Base(path), func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				value := contracts[contract].value()
				if err := json.Unmarshal(data, value); err != nil {
					t.Fatalf("decode: %v", err)
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				want, got := generic(t, data), generic(t, encoded)
				redact(contract, want)
				if !reflect.DeepEqual(want, got) {
					t.Errorf("re-encoding changed the example:\nwant %v\ngot  %v", want, got)
				}
			})
		}
	}
}

func TestExamplesMatchTheirSchema(t *testing.T) {
	contractsSchema, err := schema.Load(proto.Schema)
	if err != nil {
		t.Fatal(err)
	}
	for contract, paths := range examples(t) {
		for _, path := range paths {
			data, _ := os.ReadFile(path)
			value := generic(t, data)
			title := contracts[contract].title
			if problems := contractsSchema.Validate(title, value); len(problems) > 0 {
				t.Errorf("%s: %v", filepath.Base(path), problems)
			}
			if unknown := contractsSchema.UnknownKeys(title, value); len(unknown) > 0 {
				t.Errorf("%s: keys not in the schema: %v", filepath.Base(path), unknown)
			}
		}
	}
}

func TestDecodeChecksTheVersion(t *testing.T) {
	health := func(v string) []byte {
		return []byte(`{"v": ` + v + `, "status": "ok", "problems": [], "vm": "stopped", "phase": "idle", "checkedAt": 1}`)
	}
	decode := func(data []byte) (*proto.GatewayHealth, error) {
		return proto.Decode[proto.GatewayHealth](data, "gateway-health", proto.GatewayHealthVersion, "devbox")
	}

	got, err := decode(health("1"))
	if err != nil || got.Status != proto.GatewayHealthStatusOk || got.VM != proto.VMStateStopped {
		t.Fatalf("current version: %+v, %v", got, err)
	}

	cases := []struct{ data, want string }{
		{string(health("2")), "gateway-health v2 is not supported: devbox reads gateway-health v1; update devbox"},
		{string(health("0")), "gateway-health v0 is not supported: devbox reads gateway-health v1; update the program that sent it"},
		{`{"status": "ok"}`, `gateway-health: no version field "v"`},
	}
	for _, c := range cases {
		if _, err := decode([]byte(c.data)); err == nil || err.Error() != c.want {
			t.Errorf("got %v\nwant %s", err, c.want)
		}
	}
	var versionErr *proto.VersionError
	if _, err := decode(health("2")); !errors.As(err, &versionErr) || versionErr.Got != 2 || versionErr.Want != 1 {
		t.Errorf("want a *VersionError with both versions, got %#v", err)
	}
	for _, malformed := range []string{`not json`, `{"v": 1}`, `{"v": 1, "status": "fine", "problems": [], "vm": "stopped", "phase": "idle", "checkedAt": 1}`} {
		if _, err := decode([]byte(malformed)); err == nil || !strings.HasPrefix(err.Error(), "gateway-health: ") {
			t.Errorf("%s: got %v", malformed, err)
		}
	}
}

func TestDecodeIgnoresUnknownFields(t *testing.T) {
	data := []byte(`{"v": 1, "status": "ok", "problems": [], "vm": "running", "phase": "idle", "checkedAt": 5,
		"addedLater": {"nested": true}, "alsoNew": [1, 2]}`)
	got, err := proto.Decode[proto.GatewayHealth](data, "gateway-health", proto.GatewayHealthVersion, "devbox")
	if err != nil || got.CheckedAt != 5 || got.VM != proto.VMStateRunning {
		t.Fatalf("%+v, %v", got, err)
	}
}
