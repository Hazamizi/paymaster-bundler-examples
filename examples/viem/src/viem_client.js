import config from "../../../config.js";
import { createPublicClient, http } from "viem";

const localChain = {
    id: 13, // or whatever your local chain uses
    name: "Local",
    network: "local",
    rpcUrls: {
      default: {
        http: ["http://localhost:8549"],
      },
    },
  }
export const chain = localChain; // base or baseSepolia
export const client = createPublicClient({
  chain: chain,
  transport: http(config.rpc_url),
});
