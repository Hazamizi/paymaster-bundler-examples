package entrypoint

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

var (
	EntryPointV06 = common.HexToAddress("0x5ff137d4b0fdcd49dca30c7cf57e578a026d2789")
	EntryPointV07 = common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")
	EntryPointV08 = common.HexToAddress("0x4337084d9e255ff0702461cf8895ce9e3b5ff108")

	EntryPointVersions = map[string]common.Address{
		"v0.6": EntryPointV06,
		"v0.7": EntryPointV07,
		// "v0.8": EntryPointV08,
	}
)

// ValidateAndParseEntryPointVersions validates version strings and returns their addresses
func ValidateAndParseEntryPointVersions(versions string) ([]common.Address, error) {
	if versions == "" {
		return nil, fmt.Errorf("no entry point versions provided")
	}

	versionList := strings.Split(versions, ",")
	addresses := make([]common.Address, 0, len(versionList))
	seenVersions := make(map[string]bool)

	for _, version := range versionList {
		version = strings.TrimSpace(version)
		if version == "" {
			return nil, fmt.Errorf("empty version in list")
		}

		if seenVersions[version] {
			return nil, fmt.Errorf("duplicate version: %s", version)
		}

		addr, exists := EntryPointVersions[version]
		if !exists {
			return nil, fmt.Errorf("unsupported entry point version: %s (supported versions: v0.6, v0.7)", version)
		}

		addresses = append(addresses, addr)
		seenVersions[version] = true
	}

	return addresses, nil
}
