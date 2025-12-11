package simulator

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Check7562Violations checks for violations in the validation context for ERC-7562
func (s *Simulator) check7562Violations(validationContext *ValidationContext) ([]SimulationViolation, error) {
	violations := make([]SimulationViolation, 0)

	// Process each phase (factory, account, paymaster)
	for i, phase := range validationContext.TracerOut.Phases {
		entityType := entityTypeFromSimulationPhase(i)
		if entityType == "" {
			continue
		}

		entityInfo, exists := validationContext.EntityInfos[entityType]
		if !exists {
			continue
		}

		// Check forbidden opcodes
		for _, opcode := range phase.ForbiddenOpcodesUsed {
			// Skip BALANCE and SELFBALANCE for staked entities
			if entityInfo.IsStaked && (opcode == "BALANCE" || opcode == "SELFBALANCE") {
				continue
			}

			violations = append(violations, SimulationViolation{
				Type:   "UsedForbiddenOpcode",
				Entity: entityInfo.Entity,
				Opcode: opcode,
			})
		}

		// Check external code access
		for addr, opcode := range phase.ExtCodeAccessInfo {
			if addr == validationContext.EntryPointAddress {
				violations = append(violations, SimulationViolation{
					Type:     "UsedForbiddenOpcode",
					Entity:   entityInfo.Entity,
					Contract: addr,
					Opcode:   opcode,
				})
			}
		}

		// Check forbidden precompiles
		for _, precompile := range phase.ForbiddenPrecompilesUsed {
			violations = append(violations, SimulationViolation{
				Type:       "UsedForbiddenPrecompile",
				Entity:     entityInfo.Entity,
				Precompile: precompile,
			})
		}

		// Check storage accesses
		for addr, accessInfo := range phase.StorageAccesses {
			validationContext.AccessedAddresses[addr] = true

			restrictions := parseStorageAccesses(
				&accessInfo,
				&validationContext.TracerOut.AssociatedSlotsByAddress,
				addr,
				validationContext.SenderAddress,
				validationContext.EntryPointAddress,
				validationContext.HasFactory,
				&entityInfo.Entity,
			)

			for _, restriction := range restrictions {
				switch restriction.Type {
				case "NeedsStake":
					needsStakeEntity, exists := validationContext.EntityInfos[restriction.NeedsStake]
					if !exists || !needsStakeEntity.IsStaked {
						violations = append(violations, SimulationViolation{
							Type:    "NotStaked",
							Entity:  restriction.AccessingEntity,
							Address: restriction.AccessedAddress,
						})
					}
				case "AssociatedStorageDuringDeploy":
					violations = append(violations, SimulationViolation{
						Type:    "AssociatedStorageDuringDeploy",
						Address: restriction.AccessedAddress,
					})
				case "Banned":
					violations = append(violations, SimulationViolation{
						Type:    "InvalidStorageAccess",
						Entity:  restriction.AccessingEntity,
						Address: restriction.AccessedAddress,
					})
				}
			}
		}

		// Check other phase violations
		if phase.CalledNonEntryPointWithValue {
			violations = append(violations, SimulationViolation{
				Type:   "CallHadValue",
				Entity: entityInfo.Entity,
			})
		}

		if phase.CalledBannedEntryPointMethod {
			violations = append(violations, SimulationViolation{
				Type:   "CalledBannedEntryPointMethod",
				Entity: entityInfo.Entity,
			})
		}

		if phase.RanOutOfGas {
			violations = append(violations, SimulationViolation{
				Type:   "OutOfGas",
				Entity: entityInfo.Entity,
			})
		}

		// Check undeployed contract accesses
		for _, addr := range phase.UndeployedContractAccesses {
			if entityInfo.Entity.Kind == EntityTypeFactory && addr == validationContext.SenderAddress {
				continue
			}
			violations = append(violations, SimulationViolation{
				Type:    "AccessedUndeployedContract",
				Entity:  entityInfo.Entity,
				Address: addr,
			})
		}
	}

	// Check for factory calling create2 twice
	if validationContext.TracerOut.FactoryCalledCreate2Twice {
		factoryInfo, exists := validationContext.EntityInfos[EntityTypeFactory]
		if exists {
			violations = append(violations, SimulationViolation{
				Type:    "FactoryCalledCreate2Twice",
				Address: factoryInfo.Entity.Address,
			})
		} else {
			violations = append(violations, SimulationViolation{
				Type:    "FactoryCalledCreate2Twice",
				Address: validationContext.EntryPointAddress,
			})
		}
	}

	// Check for Arbitrum Stylus contracts
	for addr, contractInfo := range validationContext.TracerOut.AccessedContracts {
		if contractInfo.Header == "0xEFF000" {
			violations = append(violations, SimulationViolation{
				Type:        "AccessedUnsupportedContractType",
				Address:     addr,
				Description: "Arbitrum Stylus",
			})
		}
	}

	return violations, nil
}

func entityTypeFromSimulationPhase(index int) EntityType {
	switch index {
	case 0:
		return EntityTypeFactory
	case 1:
		return EntityTypeAccount
	case 2:
		return EntityTypePaymaster
	default:
		return ""
	}
}

func parseStorageAccesses(
	accessInfo *AccessInfo,
	slotsbyAddress *AssociatedSlotsByAddress,
	address common.Address,
	sender common.Address,
	entrypoint common.Address,
	hasFactory bool,
	entity *Entity,
) []StorageRestriction {
	var restrictions []StorageRestriction

	// Check all reads and writes
	for slot := range accessInfo.Reads {
		// Check if slot is associated with the address
		isAssociated := slotsbyAddress.IsAssociatedSlot(address, slot)

		// Logic matching Rust implementation
		if address == entrypoint {
			// [STO-010] - Only staked entities can access entry point storage
			restrictions = append(restrictions, StorageRestriction{
				Type:            "NeedsStake",
				NeedsStake:      entity.Kind,
				AccessingEntity: *entity,
				AccessedAddress: address,
				Slot:            slot,
			})
		} else if address == sender && isAssociated {
			if hasFactory {
				// [STO-021] - Factory must be staked to access sender's associated storage
				restrictions = append(restrictions, StorageRestriction{
					Type:            "NeedsStake",
					NeedsStake:      EntityTypeFactory,
					AccessingEntity: *entity,
					AccessedAddress: address,
					Slot:            slot,
				})
			} else {
				// [STO-022] - Associated storage access during deployment requires stake
				restrictions = append(restrictions, StorageRestriction{
					Type:            "AssociatedStorageDuringDeploy",
					AccessedAddress: address,
					Slot:            slot,
				})
			}
		} else if isAssociated {
			// [STO-020] - Entity must be staked to access associated storage
			restrictions = append(restrictions, StorageRestriction{
				Type:            "NeedsStake",
				NeedsStake:      entity.Kind,
				AccessingEntity: *entity,
				AccessedAddress: address,
				Slot:            slot,
			})
		}
	}

	// Also check writes (same slots might need different restrictions)
	for slot := range accessInfo.Writes {
		if address == entrypoint {
			// [STO-011] - Cannot write to entry point storage
			restrictions = append(restrictions, StorageRestriction{
				Type:            "Banned",
				AccessedAddress: address,
				Slot:            slot,
			})
		}
	}

	return restrictions
}

func (a *AssociatedSlotsByAddress) IsAssociatedSlot(address common.Address, slot string) bool {
	// Convert slot to big.Int for comparison
	slotBig := new(big.Int)
	slotBig.SetString(slot, 0) // base 0 for auto-detection
	if slotBig == nil {
		return false
	}

	// Check if slot equals the address (converted to U256)
	addrBig := new(big.Int).SetBytes(address.Bytes())
	if slotBig.Cmp(addrBig) == 0 {
		return true
	}

	// Get associated slots for this address
	associatedSlots, exists := a.Slots[address.String()]
	if !exists {
		return false
	}

	// Find the next smallest slot
	var nextSmallestSlot *big.Int
	slotPlusOne := new(big.Int).Add(slotBig, big.NewInt(1))

	for _, s := range associatedSlots {
		sBig := new(big.Int)
		_, ok := sBig.SetString(s, 0)
		if !ok {
			continue
		}

		if sBig.Cmp(slotPlusOne) < 0 { // s < slot + 1
			if nextSmallestSlot == nil || sBig.Cmp(nextSmallestSlot) > 0 {
				nextSmallestSlot = sBig
			}
		}
	}

	if nextSmallestSlot == nil {
		return false
	}

	// Check if (slot - next_smallest_slot) < 128
	diff := new(big.Int).Sub(slotBig, nextSmallestSlot)
	return diff.Cmp(big.NewInt(128)) < 0
}

// ParseValidationData extracts sigFailed, validAfter, validUntil from packed validation data
func parseValidationData(validationData *big.Int) (bool, uint64, uint64) {
	// Extract sigFailed (lowest bit)
	sigFailed := new(big.Int).And(validationData, common.Big1).Uint64() == 1

	// Extract validUntil (bits 160-208)
	validUntil := new(big.Int).Rsh(validationData, 160)
	validUntil = new(big.Int).And(validUntil, new(big.Int).Sub(new(big.Int).Lsh(common.Big1, 48), common.Big1))

	// If validUntil is 0, set to max uint48
	if validUntil.Cmp(common.Big0) == 0 {
		validUntil = new(big.Int).Sub(new(big.Int).Lsh(common.Big1, 48), common.Big1)
	}

	// Extract validAfter (bits 208-256)
	validAfter := new(big.Int).Rsh(validationData, 208)
	validAfter = new(big.Int).And(validAfter, new(big.Int).Sub(new(big.Int).Lsh(common.Big1, 48), common.Big1))

	return sigFailed, validAfter.Uint64(), validUntil.Uint64()
}
