import { encodeFunctionData, http , parseAbi} from 'viem'

import { baseSepolia } from 'viem/chains'
import { createSmartAccountClient } from 'permissionless'
import { createPimlicoClient } from "permissionless/clients/pimlico";
import { getAccount } from './account.js';
import { abi } from './abi.js';
import config from '../../../config.js';

// Get yours at https://www.coinbase.com/cloud/products/base/rpc
const rpcUrl = config.rpc_url
const tokenAddress = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
const paymasterAddress = "0xdCBE0C1A00e4Cf24AE77c52125e6e6b4F7C6Db4e"
const tokenAmount = 10000000000;
// Create the Cloud Paymaster
const cloudPaymaster = createPimlicoClient({
    chain: baseSepolia,
    transport: http(rpcUrl)
})

// Get the account
const account = await getAccount(config.account_type).catch((error) => {
    console.error("\x1b[31m", `❌ ${error.message}`);
    process.exit(1);
});

// Create the smart account for the user
const smartAccountClient = createSmartAccountClient({
    account,
    chain: baseSepolia,
    bundlerTransport: http(rpcUrl),
});

// Encode the calldata
const approve = {
    to: tokenAddress,
    data: encodeFunctionData({
        abi: parseAbi(["function approve(address,uint)"]),
        functionName: "approve",
        args: [paymasterAddress, tokenAmount],
    })
  }
console.log("\x1b[33m%s\x1b[0m", `Minting to ${account.address} (Account type: ${config.account_type})`);
console.log("Waiting for transaction...")
const calls = [approve]

const uo = await smartAccountClient.prepareUserOperation({
    account,
    calls,
    paymaster: false
  }); 

  console.log(uo)

// Send the sponsored transaction!
account.userOperation = {
    estimateGas: async (userOperation) => {
      const estimate = await smartAccountClient.estimateUserOperationGas(userOperation);
      // adjust preVerification upward 
      estimate.preVerificationGas = estimate.preVerificationGas * 2n;
      return estimate;
    },
  };

  try {
    const uo = await smartAccountClient.prepareUserOperation({
      account,
      calls,
      paymaster: false
    });
    console.log(uo)
  
    // const receipt = await smartAccountClient.waitForUserOperationReceipt({
    //   hash: userOpHash,
    // });
  
    console.log("✅ Transaction successfully sponsored!");
    console.log(`⛽ View sponsored UserOperation on blockscout: https://base-sepolia.blockscout.com/op/${receipt.userOpHash}`);
    console.log(`🔍 View NFT mint on basescan: https://sepolia.basescan.org/address/${account.address}`);
    process.exit()
  } catch (error) {
    console.log("Error sending transaction: ", error);
    process.exit(1)
  }