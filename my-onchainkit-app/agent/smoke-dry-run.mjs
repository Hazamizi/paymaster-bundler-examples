import { config } from "dotenv";
config({ path: ".env.local" });

async function main() {
  const { executeTradeSignal } = await import("../lib/tradeAgent.ts");
  const r = await executeTradeSignal({
    ticker: "ETH-EUR",
    action: "buy",
    quoteSize: "2",
    source: "smoke",
  });
  console.log(JSON.stringify(r, null, 2));
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
