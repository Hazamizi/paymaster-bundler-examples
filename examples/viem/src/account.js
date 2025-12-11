import { toCoinbaseSmartAccount } from "viem/account-abstraction";
import { privateKeyToAccount } from "viem/accounts";
import { toSimpleSmartAccount } from 'permissionless/accounts'
import { client } from "./viem_client.js ";
import config from "../../../config.js";

// const owner = privateKeyToAccount(config.private_key);
const pk = "0x9f35c4d47fb7324464239bef54d7bb5505e975a38d2a3197883c6e4148461046"
const unfundedPk = "0xf6d92121ea9bc18ff783bb07be49f54cf2d30ccf3e92294d7f63c2a5d16f04bf"
const owner = privateKeyToAccount(
    pk
  );

// export const coinbaseAccount = await toCoinbaseSmartAccount({
//   client,
//   owners: [owner],
//   version: "1",
// });

export const simpleAccountV6 = await toSimpleSmartAccount({
  client,
  owner: owner,
  entryPoint: {
    address: "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789",
    version: "0.6",
  },
});

export const simpleAccountV7 = await toSimpleSmartAccount({
    client,
    owner: owner,
    entryPoint: {
      address: "0x0000000071727De22E5E9d8BAf0edAc6f37da032",
      version: "0.7",
    },
  });
