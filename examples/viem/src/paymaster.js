import { http } from "viem";
import { createBundlerClient } from "viem/account-abstraction";
// import { simpleAccount, coinbaseAccount } from "./account.js";
import config from "../../../config.js";
import { client, chain } from "./viem_client.js";


export const bundlerClient = createBundlerClient({
  client,
  transport: http(config.rpc_url),
  chain: chain
});