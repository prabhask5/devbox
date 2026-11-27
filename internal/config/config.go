// Package config parses devbox's TOML files: the host config and secrets the gateway reads,
// the hub's config and secrets, and the dotfiles manifest. Parsing validates the whole file
// against its contract and reports every problem at once.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/schema"
)

// Error lists everything wrong with a file, one problem per line, each starting with the
// file's name and, where there is one, the key ("devbox.toml: vm.code_port: expected …").
type Error struct{ Problems []string }

func (e *Error) Error() string { return strings.Join(e.Problems, "\n") }

// Host parses the host config, /etc/devbox/devbox.toml. name is how messages refer to the
// file. Warnings are keys this version does not know and ignores.
func Host(name string, data []byte) (cfg *proto.HostConfig, warnings []string, err error) {
	return parse[proto.HostConfig](name, data, "HostConfig", proto.HostConfigVersion, func(file map[string]any) []schema.Problem {
		hosts, _ := file["hosts"].(map[string]any)
		domain, _ := file["domain"].(string)
		for _, app := range []string{"code", "agents"} {
			if _, set := hosts[app]; !set {
				hosts[app] = app + "." + domain
			}
		}
		if hosts["code"] == hosts["agents"] {
			return []schema.Problem{{Path: "hosts.agents", Message: "must differ from hosts.code"}}
		}
		return nil
	})
}

// HostSecrets parses the host secrets, /etc/devbox/secrets.toml.
func HostSecrets(name string, data []byte) (*proto.HostSecrets, []string, error) {
	return parse[proto.HostSecrets](name, data, "HostSecrets", proto.HostSecretsVersion, nil)
}

// Hub parses the hub config, ~/.config/devbox/hub.toml.
func Hub(name string, data []byte) (*proto.HubConfig, []string, error) {
	return parse[proto.HubConfig](name, data, "HubConfig", proto.HubConfigVersion, nil)
}

// HubSecrets parses the hub secrets, ~/.config/devbox/hub.secrets.toml.
func HubSecrets(name string, data []byte) (*proto.HubSecrets, []string, error) {
	return parse[proto.HubSecrets](name, data, "HubSecrets", proto.HubSecretsVersion, nil)
}

// DotfilesManifest parses devbox.toml from the root of a dotfiles repository.
func DotfilesManifest(name string, data []byte) (*proto.DotfilesManifest, []string, error) {
	return parse[proto.DotfilesManifest](name, data, "DotfilesManifest", proto.DotfilesManifestVersion, nil)
}

// Duration converts a duration that passed validation ("15m", "90s", "1h30m").
func Duration(value string) (time.Duration, error) { return time.ParseDuration(value) }

var contracts = sync.OnceValues(func() (*schema.Schema, error) { return schema.Load(proto.Schema) })

// parse runs the steps every file goes through. finish, when given, runs on the valid file
// with defaults applied: it fills in computed defaults and reports problems across keys.
func parse[T any](name string, data []byte, typ string, version int, finish func(map[string]any) []schema.Problem) (*T, []string, error) {
	fail := func(problems ...string) (*T, []string, error) {
		for i := range problems {
			problems[i] = name + ": " + problems[i]
		}
		return nil, nil, &Error{problems}
	}
	contracts, err := contracts()
	if err != nil {
		return nil, nil, err
	}

	var file map[string]any
	if err := toml.Unmarshal(data, &file); err != nil {
		var syntax toml.ParseError
		if errors.As(err, &syntax) {
			return fail(fmt.Sprintf("line %d: %s", syntax.Position.Line, syntax.Message))
		}
		return fail(err.Error())
	}
	if dates := findDates(file, ""); len(dates) > 0 {
		sort.Strings(dates)
		for i := range dates {
			dates[i] += ": dates are not supported"
		}
		return fail(dates...)
	}

	if got, ok := file["version"].(int64); ok && got != int64(version) {
		fix := fmt.Sprintf("update the file to version %d", version)
		if got > int64(version) {
			fix = "update devbox"
		}
		return fail(fmt.Sprintf("version %d is not supported (this devbox reads version %d); %s", got, version, fix))
	}
	problems := contracts.Validate(typ, file)
	if len(problems) == 0 {
		contracts.ApplyDefaults(typ, file)
		if finish != nil {
			problems = finish(file)
		}
	}
	if len(problems) > 0 {
		lines := make([]string, len(problems))
		for i, p := range problems {
			lines[i] = p.String()
		}
		return fail(lines...)
	}

	var warnings []string
	for _, path := range contracts.UnknownKeys(typ, file) {
		warnings = append(warnings, fmt.Sprintf("%s: %s: unknown key, ignored", name, path))
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	var out T
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	return &out, warnings, nil
}

// findDates returns the paths of TOML date and time values, which no contract uses.
func findDates(value any, path string) []string {
	var paths []string
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			paths = append(paths, findDates(child, childPath)...)
		}
	case []any:
		for i, child := range v {
			paths = append(paths, findDates(child, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case string, bool, int64, float64:
	default:
		paths = append(paths, path)
	}
	return paths
}
