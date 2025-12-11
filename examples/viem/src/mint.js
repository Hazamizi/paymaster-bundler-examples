import { bundlerClient } from "./paymaster.js";
import { simpleAccountV6 } from "./account.js";
import { abi } from "./abi.js";
import config from "../../../config.js";
const nftContractAddress = config.contract_address;
const account = simpleAccountV6;

const logs = [];
const startTime = Date.now();

const logWithTiming = (message, elapsed = Date.now() - startTime) => {
  logs.push({
    timestamp: Date.now(),
    isoTime: new Date().toISOString(),
    message: message,
    elapsed: elapsed
  });
  console.log(`[${new Date().toISOString()}] ${message} (took ${elapsed}ms)`);
};

const printSortedLogs = () => {
  logs.sort((a, b) => a.timestamp - b.timestamp);
  console.log("\nOperation Log Summary:");
  console.log("--------------------");
  logs.forEach(log => {
    console.log(`[${log.isoTime}] ${log.message}`);
  });
};

account.userOperation = {
  estimateGas: async (userOperation) => {
    logWithTiming("Estimating gas...");
    const estimate = await bundlerClient.estimateUserOperationGas(
      userOperation
    );
    estimate.preVerificationGas = estimate.preVerificationGas * BigInt(2);

    logWithTiming("Gas estimation completed");
    return estimate;
  },
};

try {
  logWithTiming("Sending user operation...");
  const userOpHash = await bundlerClient.sendUserOperation({
    account: account,
    calls: [
      {
        abi: abi,
        functionName: "mintTo",
        to: nftContractAddress, //nftContractAddress, or udsctestnet
        args: [account.address, 1]//[account.address, 10000],
      },
    ],
    paymaster: false
  });

  const startTimeForReceipt = Date.now();

  logWithTiming(`User operation sent with hash: ${userOpHash}`, );

  logWithTiming("Waiting for receipt...");
  const receipt = await bundlerClient.waitForUserOperationReceipt({
    hash: userOpHash,
    pollingInterval: 150,
  });

//   console.log(receipt);
  logWithTiming("Receipt received");
  logWithTiming("Receipt received after: ", Date.now() - startTimeForReceipt);

  logWithTiming("✅ Transaction successfully sponsored!");

  console.log("receipt: ", receipt);
  
  const totalTime = Date.now() - startTime;
  logs.push({
    timestamp: Date.now(),
    isoTime: new Date().toISOString(),
    message: `Total execution time: ${totalTime}ms`,
    elapsed: totalTime
  });
  console.log(`[${new Date().toISOString()}] Total execution time: ${totalTime}ms`);
  
  // Sort and print logs by timestamp
  logs.sort((a, b) => a.timestamp - b.timestamp);
  console.log("\nOperation Log Summary:");
  console.log("--------------------");
  logs.forEach(log => {
    console.log(`[${log.isoTime}] ${log.message}`);
  });
  
  process.exit();
} catch (error) {
  logWithTiming(`Error sending transaction: ${error.message}`);
  console.error(error);
  
  // Print sorted logs even on error
  printSortedLogs();
  
  process.exit(1);
}
