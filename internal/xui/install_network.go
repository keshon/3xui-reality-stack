package xui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

const networkListTemplate = `{"id":{{json .ID}},"name":{{json .Name}}}`
const networkInspectTemplate = `{"id":{{json .Id}},"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"internal":{{json .Internal}},"ipv6":{{json .EnableIPv6}},"containers":{{len .Containers}},"endpoints":{{json .Containers}},"installation":{{json (index .Labels "io.stackctl.installation")}},"operation":{{json (index .Labels "io.stackctl.operation")}},"subnets":{{json .IPAM.Config}}}`

type installNetwork struct {
	ID           string                             `json:"id"`
	Name         string                             `json:"name"`
	Driver       string                             `json:"driver"`
	Scope        string                             `json:"scope"`
	Internal     bool                               `json:"internal"`
	IPv6         bool                               `json:"ipv6"`
	Containers   int                                `json:"containers"`
	Endpoints    map[string]json.RawMessage         `json:"endpoints"`
	Installation string                             `json:"installation"`
	Operation    string                             `json:"operation"`
	Subnets      []struct{ Subnet, Gateway string } `json:"subnets"`
}

func installationOwner(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])
}

func observeInstallNetwork(ctx context.Context, run dockerCommand) (*installNetwork, error) {
	data, err := run(ctx, "network", "ls", "--no-trunc", "--format", networkListTemplate)
	if err != nil {
		return nil, errors.New("cannot inspect installation network inventory")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var id string
	for {
		var row struct{ ID, Name string }
		err := d.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil || !dockerID(row.ID) || row.Name == "" {
			return nil, errors.New("invalid installation network inventory")
		}
		if row.Name == "proxy" {
			if id != "" {
				return nil, errors.New("installation network name is ambiguous")
			}
			id = row.ID
		}
	}
	if id == "" {
		return nil, nil
	}
	data, err = run(ctx, "network", "inspect", id, "--format", networkInspectTemplate)
	var network installNetwork
	if err != nil || json.Unmarshal(data, &network) != nil || network.ID != id {
		return nil, errors.New("cannot inspect immutable installation network")
	}
	return &network, nil
}

func ownedNetwork(network *installNetwork, root, owner, id string) bool {
	return ownedNetworkConfiguration(network, root, owner, id) && network.Containers == 0
}

func ownedNetworkConfiguration(network *installNetwork, root, owner, id string) bool {
	return network != nil && dockerID(network.ID) && (id == "" || network.ID == id) && network.Name == "proxy" && network.Driver == "bridge" && network.Scope == "local" && !network.Internal && !network.IPv6 && network.Installation == installationOwner(root) && network.Operation == owner && len(network.Subnets) == 1 && network.Subnets[0].Subnet == "172.28.0.0/16" && network.Subnets[0].Gateway == "172.28.0.1"
}

func ensureInstallNetwork(ctx context.Context, run dockerCommand, root, owner, expectedID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, err := hex.DecodeString(owner); err != nil || len(id) != 16 {
		return "", errors.New("invalid installation network owner")
	}
	network, err := observeInstallNetwork(ctx, run)
	if err != nil {
		return "", err
	}
	if network == nil {
		if expectedID != "" {
			return "", errors.New("owned installation network disappeared; replacement is blocked")
		}
		_, _ = run(ctx, "network", "create", "--driver", "bridge", "--subnet", "172.28.0.0/16", "--gateway", "172.28.0.1", "--label", "io.stackctl.installation="+installationOwner(root), "--label", "io.stackctl.operation="+owner, "proxy")
		network, err = observeInstallNetwork(ctx, run)
		if err != nil {
			return "", err
		}
	}
	if !ownedNetwork(network, root, owner, expectedID) {
		return "", errors.New("installation network is absent, foreign, changed or attached to unexpected containers")
	}
	return network.ID, nil
}
