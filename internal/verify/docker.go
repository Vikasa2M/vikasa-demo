package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// CabinetLeafService derives the compose service name of a cabinet's leaf
// NATS container from its cabinet id, e.g. "cab-i85-001" ->
// "mardot-cab-i85-001-nats". This is the CABINET LEAF, not the sim: cutting
// the sim's own container would stop it from buffering anything, defeating
// the point of the demo (the edge is supposed to keep buffering through the
// outage). Cutting the leaf NATS container's network instead severs the
// path upstream while leaving the sim free to keep publishing into its
// local JetStream buffer.
func CabinetLeafService(cabinet string) string {
	return fmt.Sprintf("mardot-%s-nats", cabinet)
}

// ComposeContainer is the subset of `docker compose ps --format json`
// fields democtl needs to resolve a service's container name and network
// without hardcoding either.
type ComposeContainer struct {
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	Networks string `json:"Networks"`
	Project  string `json:"Project"`
}

// ParseComposePSAll parses the output of `docker compose ps --format json`
// (one JSON object per line — NDJSON, not a single JSON array) into every
// container it describes.
func ParseComposePSAll(output []byte) ([]ComposeContainer, error) {
	lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	containers := make([]ComposeContainer, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var c ComposeContainer
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("verify: parse compose ps line %q: %w", line, err)
		}
		containers = append(containers, c)
	}
	return containers, nil
}

// FindService returns the container running the given service from a
// parsed `docker compose ps` listing. Returns an error if the service isn't
// present.
func FindService(containers []ComposeContainer, service string) (ComposeContainer, error) {
	for _, c := range containers {
		if c.Service == service {
			return c, nil
		}
	}
	return ComposeContainer{}, fmt.Errorf("verify: service %q not found in compose ps output", service)
}

// ParseComposePS parses `docker compose ps --format json` output and
// returns the container running the given service in one call. Equivalent
// to ParseComposePSAll followed by FindService.
func ParseComposePS(output []byte, service string) (ComposeContainer, error) {
	containers, err := ParseComposePSAll(output)
	if err != nil {
		return ComposeContainer{}, err
	}
	return FindService(containers, service)
}

// WANNetworkKey is the compose network key (as declared in
// docker-compose.yml's top-level `networks:` block) for the shared WAN-side
// network linking every DOT's regional/central/dmz tier to each cabinet
// leaf's uplink. This is the network `democtl cut`/`restore` must act on —
// a cabinet leaf (e.g. mardot-cab-i85-001-nats) is also attached to its
// private per-cabinet LAN network (e.g. cab-i85-001-net), which must stay
// up across a cut so the sim keeps buffering at the edge. See
// deploy/compose/docker-compose.yml.
const WANNetworkKey = "vikasa"

// matchNetwork finds, among a container's (possibly several) attached
// networks, the one compose registered under the given key (e.g.
// "vikasa"). Compose names the underlying docker network
// "<project>_<key>" by default (e.g. "vikasa-demo_vikasa"), so this
// matches either an exact key match (external/pre-named networks) or a
// "_<key>" suffix, without assuming a specific project-name prefix and
// without assuming the container has only one network.
func matchNetwork(c ComposeContainer, key string) (string, bool) {
	for _, raw := range strings.Split(c.Networks, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if name == key || strings.HasSuffix(name, "_"+key) {
			return name, true
		}
	}
	return "", false
}

// ResolveNetwork finds the compose network registered under key (pass
// WANNetworkKey for the shared "vikasa" network) to act on for target.
// Normally this is just one of target's own attached networks — but once a
// container has already been `docker network disconnect`-ed from that
// specific network (the state `democtl restore` runs against), target's
// Networks field no longer lists it at all, even though target may still be
// attached to OTHER networks (a cabinet leaf stays on its private cabinet
// LAN network across a cut). In that case, fall back to any OTHER container
// from the same compose listing that is still attached to a network
// matching key. Every WAN-side service shares the single "vikasa" network,
// so a sibling's membership reveals the exact (possibly project-prefixed)
// name to reconnect target to, without hardcoding it — but the match is by
// key, not "any network of any sibling", since siblings can now differ
// (e.g. a cabinet sim is attached ONLY to its private cabinet-net, never to
// "vikasa", and must not be mistaken for a source of the WAN network name).
func ResolveNetwork(containers []ComposeContainer, target ComposeContainer, key string) (string, error) {
	if name, ok := matchNetwork(target, key); ok {
		return name, nil
	}
	for _, c := range containers {
		if c.Name == target.Name {
			continue
		}
		if name, ok := matchNetwork(c, key); ok {
			return name, nil
		}
	}
	return "", fmt.Errorf(
		"verify: could not resolve network %q for %q (target has no matching network, and no sibling container in the compose listing is attached to one)",
		key, target.Name)
}
