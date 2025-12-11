package simulator

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func TestParseOverrides(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	balance1 := (*hexutil.Big)(big.NewInt(1000000000000000000)) // 1 ETH

	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	nonce2 := hexutil.Uint64(10)

	tests := []struct {
		name          string
		overridesJson []byte
		wantOverrides *OverrideSet
		wantErr       bool
	}{
		{
			name: "Valid single override - balance",
			overridesJson: []byte(`{
				"0x1111111111111111111111111111111111111111": {
					"balance": "0xde0b6b3a7640000" 
				}
			}`),
			wantOverrides: &OverrideSet{
				addr1: OverrideAccount{
					Balance: balance1,
				},
			},
			wantErr: false,
		},
		{
			name: "Valid multiple overrides - balance and nonce",
			overridesJson: []byte(`{
				"0x1111111111111111111111111111111111111111": {
					"balance": "0xde0b6b3a7640000" 
				},
				"0x2222222222222222222222222222222222222222": {
					"nonce": "0xa"
				}
			}`),
			wantOverrides: &OverrideSet{
				addr1: OverrideAccount{
					Balance: balance1,
				},
				addr2: OverrideAccount{
					Nonce: &nonce2,
				},
			},
			wantErr: false,
		},
		{
			name:          "Invalid JSON",
			overridesJson: []byte(`{invalid json}`),
			wantOverrides: nil,
			wantErr:       true,
		},
		{
			name:          "Empty JSON object",
			overridesJson: []byte(`{}`),
			wantOverrides: &OverrideSet{},
			wantErr:       false,
		},
		{
			name:          "Nil input",
			overridesJson: nil,
			wantOverrides: nil, // json.Unmarshal handles nil as invalid syntax
			wantErr:       true,
		},
		{
			name:          "Empty input",
			overridesJson: []byte{},
			wantOverrides: nil, // json.Unmarshal handles empty as invalid syntax
			wantErr:       true,
		},
		{
			name: "Invalid balance format",
			overridesJson: []byte(`{
				"0x1111111111111111111111111111111111111111": {
					"balance": "not_a_hex_number" 
				}
			}`),
			wantOverrides: nil, // Expecting unmarshal error due to hexutil.Big parsing
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOverrides, err := ParseOverrides(tt.overridesJson)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseOverrides() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			// Use reflect.DeepEqual for comparison, handles pointers and nested structures
			if !reflect.DeepEqual(gotOverrides, tt.wantOverrides) {
				// Provide more detailed diff in case of mismatch
				t.Errorf("ParseOverrides() gotOverrides = %v, want %v", gotOverrides, tt.wantOverrides)
				// Optional: Log the detailed structs if needed for debugging
				// t.Logf("Got: %+v", gotOverrides)
				// t.Logf("Want: %+v", tt.wantOverrides)
				// if gotOverrides != nil && tt.wantOverrides != nil {
				//    for k, v := range *gotOverrides {
				//      t.Logf("Got Key %s: %+v", k, v)
				//    }
				//     for k, v := range *tt.wantOverrides {
				//      t.Logf("Want Key %s: %+v", k, v)
				//    }
				// }
			}
		})
	}
}
